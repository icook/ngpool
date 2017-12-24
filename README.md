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
