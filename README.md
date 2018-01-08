# ngpool

**Notice: This is pre-v1 software, it is not feature complete or documented.**

ngpool is a suite of services for hosting a cryptocurrency mining pool. It is
the second generation of the simplecoin suite of tools. These services use etcd
as a central configuration store and service discovery mechanism.

### ngstratum
A stratum mining server. One port per process. Aux proof of work (merged-mining) support.

### ngweb
Provides a REST API for end user interaction and management. Also contains all crontabs.

### ngcoinserver
Runs alongside all "coinservers", or bitcoind-like processes. It is a thin
wrapper providing block notification pubsub, status monitoring and service
announcement. 

### ngsigner
Signs raw transactions that ngweb produces to payout users. This is a simple
utility that allows easy separate of private keys from main pool servers for
added security. Does not require running coinservers on payout machine.

### ngctl
A commandline utility for managing service configuration.

## Motivations

Simplecoin had design shortcomings that made operational complexity very high.
Ngpool addresses these in the following ways:

**Configuration Complexity**
A full simplecoin installation might support a dozen currencies, each with a
public and private coinserver (bitcoind), and then also a payout coinserver. In
addition, there would likely be a dedicated stratum mining port for each
currency. Each service had their own config, and some information (network/coin
specifics, etc) were duplicated between configurations. This quickly became
tedious to manage.

Ngpool changes addressing this problem with:
* Service discovery. Connections between stratum ports and coinservers is done
  automatically, no configuration necessary, no restart required.
* Shared configurations. No duplication when configuring services, as currency
  configuration and sharechain configuration is global.
* Defaults. Configure site wide defaults for stratum, coinservers, etc.

**Poor load tolerence**
Simplecoin stratum servers struggled to stay low latency with high levels of
concurrency. powerpool was written in Python with Gevent, which is a great
tool, but frankly it only stretches Python so far. Ngpool written in golang is
substantially quicker in certain critical areas.

**Difficult themeing**
Changing the UI to theme it for different deployments was challenging because
the backend logic and frontend UI were tightly coupled with server side
rendering in flask. UI is now a client side React application, and the backend
is simply a REST api, so altering the frontend should be significantly simpler.

**Poor testing coverage**
Many important parts of simplecoin were not written with testing in mind.
Combined with the complexity of service interactions, lack of testing coverage
lead to sluggish release intervals (lots of manual testing) as the codebase
grew. Refactoring and improving many areas of code became "not worth the
headache" because the manual testing required afterward.

# Install

This is basic documentation for setting up a basic pool on Ubuntu. From a root (sudo -s) prompt:

``` bash
# Install postgresql and other extras
root$ apt-get update
root$ apt-get install postgresql-9.6 git

# We require at least etcd 2.2.6 because of a bugfix. Check officail repo version.  
root$ apt-cache show etcd

# If result of above not > 2.2.6, install from snap
root$ apt-get install snapd
root$ snap install etcd
root$ cp /snap/etcd/current/etcd.conf.yml.sample /var/snap/etcd/common/etcd.conf.yml
root$ snap start etcd

# If etcd from repo is >2.2.6, just install it from the official repos
root$ apt-get install etcd

# Confirm etcd has started now. This should not error out
root$ etcdctl ls

# Create our postgresql user. Generate password with apg or similar secure generator
root$ su - postgres
postgres$ createuser --pwprompt ngpool  # enter password, don't loose it
postgres$ createdb -O ngpool ngpool
postgres$ <Ctrl-D>

# Create a new user, disallow login from all sources
root$ adduser ngpool
root$ usermod --expiredate 1 ngpool
root$ su - ngpool

# Download the latest release
root$ wget <url of release targz>
root$ tar xvzf ngpool.tar.gz
root$ cp ngpool/* /usr/local/bin/

# Download litecoin, install litecoind and litecoin-cli
root$ wget https://download.litecoin.org/litecoin-0.14.2/linux/litecoin-0.14.2-x86_64-linux-gnu.tar.gz
root$ tar xvzf litecoin-0.14.2-x86_64-linux-gnu.tar.gz
root$ cp litecoin-0.14.2/bin/litecoind /usr/local/bin/
root$ cp litecoin-0.14.2/bin/litecoin-cli /usr/local/bin/

# Generate a SubsidyAddress (where blocks are mined to) for the pool. Keep a backup of the private key somewhere safe if for production!!!
root$ ngcoinserver genkey litecoind -n test
```

Now write a simple keyfile for the payout signer to use. Ngpool separates
signing of payouts to users so it can be run from a different machine.
Currently there is no facility for encrypting this file.

``` bash
root$ nano keys.yaml
```

``` yaml
LTC_T:
    keys:
        - [YOUR GENERATED PRIVATE KEY]
```

Setup a basic common config. This is configuration that all services use, like
details about currency constants, etc. Replace the placeholder values with your
own.

``` bash 
root$ ngctl common edit
```

``` yaml
api:
    CORSOrigins: "*"  #DONT RUN IN PROD
    DbConnectionString: "user=ngpool dbname=ngpool sslmode=disable password=[YOUR DATABASE PASSWORD]"
stratum:
    DbConnectionString: "user=ngpool dbname=ngpool sslmode=disable password=[YOUR DATABASE PASSWORD]"
ShareChains:
    "LTC_T":
        fee: 0.01
        payoutmethod: "pplns"
        algo: "scrypt"
Currencies:
    "LTC_T":
        subsidyAddress: "[YOUR GENERATED PUBLIC ADDRESS HERE]"
        powalgorithm: "scrypt"
        pubkeyaddrid: "6f"
        privkeyaddrid: "ef"
        netmagic: 0xfdd2c8f1
        blockmatureconfirms: 100
        payouttransactionfee: 110
        minimumrelayfee: 100000

```

Setup a stratum config

``` bash
root$ ngctl stratum edit 3333
```

``` yaml
loglevel: info
sharechainname: LTC_T
basecurrency:
    currency: LTC_T
    algo: scrypt
    templatetype: getblocktemplate
```

Setup a coinserver config to run litcoind

``` bash
root$ ngctl coinserver edit ltc1
```

``` yaml
blocklistenerbind: 127.0.0.1:3010
coinserverbinary: litecoind
currencycode: LTC_T
eventlistenerbind: 127.0.0.1:4010
hashingalgo: scrypt
loglevel: info
nodeconfig:
  port: "19010"
  rpcpassword: "123"
  rpcport: "19011"
  rpcuser: admin1
  server: "1"
  testnet: "1"
  datadir: "~/.litecoin"
```

Now you can run each component in their own terminal like such:

``` bash
ngstratum run 3333
ngcoinserver run ltc1
ngweb run
```

With a cpuminer you can begin mining blocks...

``` bash
minerd -a scrypt -o stratum+tcp://127.0.0.1:3333 -R 3 -D -u myusername
```

Once blocks are solved, run check their confirmations and generate credits to payout users.

``` bash
ngweb confirmblocks
```

And then send payouts. This script could be run from a different machine and
target the public web API to perform payouts out of band.

``` bash
ngsign http://localhost:3000 keys
```
