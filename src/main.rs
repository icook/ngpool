extern crate reqwest;
#[macro_use] extern crate serde_json;
extern crate hyper;
extern crate base64;
extern crate hex;
extern crate crypto;
extern crate bitcoin;
extern crate byteorder;

use std::iter;
use reqwest::header::{Headers, UserAgent, Authorization, ContentType};
use hex::{FromHex, ToHex};
use crypto::digest::Digest;
use crypto::sha2::Sha256;
use bitcoin::util::base58::ToBase58;
use byteorder::{LittleEndian, WriteBytesExt};


struct Job {
    id: String,
    hash_prev: String,
    coinbase1: String,
    coinbase2: String,
    merkle_branches: Vec<String>,
    block_version: String,
    n_bits: String,
    n_time: String,
    flush: bool
}

struct RpcConnection {
    id: i32,
    url: String,
    client: reqwest::Client,
    headers: Headers
}

impl RpcConnection {
    fn new(url: &str, username: &str, password: &str) -> RpcConnection {
        let client = reqwest::Client::new();
        let mut headers = Headers::new();
        let encoded_auth = base64::encode(&format!("{}:{}", username, password));
        headers.set(UserAgent::new("NGpool/0.1"));
        headers.set(Authorization(format!("Basic {}", encoded_auth)));
        headers.set(ContentType::json());
        RpcConnection {
            client: client,
            id: 1,
            headers: headers,
            url: url.to_string()
        }
    }
    fn cmd(&mut self, method: &str, params: ()) -> Result<serde_json::Value, reqwest::Error> {
        let body = json!({
            "version": "1.1",
            "method": method,
            "params": params,
            "id": self.id
        }).to_string();
        let mut resp = self.client.post(self.url.as_str()).
                                   body(body).
                                   headers(self.headers.clone()).
                                   send().unwrap();
        self.id += 1;
        let res: serde_json::Value = resp.json()?;
        Ok(res["result"].clone())
    }
}

fn sha256d(input: Vec<u8>) -> Vec<u8> {
    let mut hasher = crypto::sha2::Sha256::new();
    hasher.input(input.as_slice());
    let mut t: [u8; 32] = [0; 32];
    hasher.result(&mut t);
    hasher.reset();
    hasher.input(&t);
    hasher.result(&mut t);
    t.to_vec()
}

fn reverse_8byte(input: &str) -> Vec<u8> {
	let i = input.to_string().into_bytes();
	i.chunks(8).rev().collect::<Vec<_>>().concat()
}

fn merklebranch(input: Vec<Vec<u8>>) -> Vec<Vec<u8>> {
    let empty_vec: Vec<u8> = vec![];

    let mut branch: Vec<Vec<u8>> = vec![];
    let mut walker = input.clone();
    walker.insert(0, empty_vec.clone());

    while walker.len() > 1 {
        let mut new: Vec<Vec<u8>> = vec![];
        branch.push(walker[1].clone());
        for slc in walker.chunks(2) {
            if slc[0] == empty_vec {
                new.push(empty_vec.clone());
            } else {
                let mut buf = empty_vec.clone();
                buf.append(&mut slc[0].clone());
                if slc.len() < 2 {
                    buf.append(&mut slc[0].clone());
                } else {
                    buf.append(&mut slc[1].clone());
                }
                let res = sha256d(buf);
                new.push(res);
            }
        }
        walker = new;
    }
    branch
}

fn varlen_encode(val: u64) -> Vec<u8> {
    let mut wtr = vec![];
    match val {
        0...0xFC             => {
            wtr.write_u8(val as u8).unwrap();
        }
        0xFD...0xFFFF        => {
            wtr.write_u8(0xFD).unwrap();
            wtr.write_u16::<LittleEndian>(val as u16).unwrap();
        }
        0x10000...0xFFFFFFFF => {
            wtr.write_u8(0xFE).unwrap();
            wtr.write_u32::<LittleEndian>(val as u32).unwrap();
        }
        _                    => {
            wtr.write_u8(0xFF).unwrap();
            wtr.write_u64::<LittleEndian>(val).unwrap();
        }
    }
    wtr
}

fn gbt_to_job(address: &str, extranonce_sz: u8, input: serde_json::Value) -> Job {
    let mut coinbase1: Vec<u8> = vec![];
    let mut coinbase2: Vec<u8> = vec![];
    let address = address.as_bytes().to_vec().to_base58();
    // Transaction version number
    coinbase1.append(&mut Vec::from_hex("00000001").unwrap());
    // TXIN Count
    coinbase1.append(&mut varlen_encode(1));

     // Coinbase input
     // ////#####################
     // Previous input. Coinbase is null, special input
     coinbase1.append(&mut [0 as u8; 32].to_vec());
     // Previous input index in transaction. All 1s, special again
     coinbase1.append(&mut [0xFF as u8; 4].to_vec());
 
     // Script
     // --------------------
     // Length of script
     coinbase1.append(&mut varlen_encode(4 + extranonce_sz as u64));
     // Height of block, required by BIP34. 4 byte fixed width
     let height = input["height"].as_u64().expect("No height from server!");
     coinbase1.write_u64::<LittleEndian>(height).unwrap();
     // --------------------
     // #######################
     // TXOut Count
    coinbase1.append(&mut varlen_encode(1));
 
 
     // Output
     // #######################
     // Output in satoshis, 8 bytes
     coinbase2.append(coinbaseVal.toHex(16).hexToBytes
// 
//     // Script length
//     // ----------------------------
//     let script = "\x76\xa9\x14" & address & "\x88\xac"
//     coinbase2.append(script.len.varlenEncode
//     coinbase2.append(script
//     // ----------------------------
//     // #######################
// 
//     // Sequence number
//     coinbase2.append("\x00".repeat(4)
    Job {
        id: "".to_string(),
        hash_prev: "".to_string(),
        coinbase1: "".to_string(),
        coinbase2: "".to_string(),
        merkle_branches: ["".to_string()].to_vec(),
        block_version: "".to_string(),
        n_bits: "".to_string(),
        n_time: "".to_string(),
        flush: true
    }
}

fn main() {
    let mut con = RpcConnection::new("http://127.0.0.1:20001", "admin1", "123");
    let result = con.cmd("getblocktemplate", ()).unwrap();
	
    println!("{:?}", result.to_string());
}

#[test]
fn reverse_test() {
	let res = reverse_8byte("ce322646a85ff6afbda7e43a3129a440d40600a6628890c1d524a0e5987045eb");
	let as_string = std::str::from_utf8(res.as_slice()).unwrap();
	assert_eq!("987045ebd524a0e5628890c1d40600a63129a440bda7e43aa85ff6afce322646", as_string)
}

#[test]
fn sha256d_test() {
    let genesis = Vec::from_hex("0100000081cd02ab7e569e8bcd9317e2fe99f2de44d49ab2b8851ba4a308000000000000e320b6c2fffc8d750423db8b1eb942ae710e951ed797f7affc8892b0f1fc122bc7f5d74df2b9441a42a14695").unwrap();
    let o = sha256d(genesis);
    println!("{:?}", o);
    assert_eq!(o.as_slice().to_hex(), "1dbd981fe6985776b644b173a4d0385ddc1aa2a829688d1e0000000000000000");
}

#[test]
fn getblocktemplate_assemble() {
    let input = json!({"bits":"207fffff","capabilities":["proposal"],"coinbaseaux":{"flags":""},"coinbasevalue":5000000000,"curtime":1507944775,"height":66,"longpollid":"95059ba8a586118eb41196fc62170ca3818e16cdbcab9f16e821274099dccae83","mintime":1507244523,"mutable":["time","transactions","prevblock"],"noncerange":"00000000ffffffff","previousblockhash":"95059ba8a586118eb41196fc62170ca3818e16cdbcab9f16e821274099dccae8","rules":[],"sigoplimit":20000,"sizelimit":1000000,"target":"7fffff0000000000000000000000000000000000000000000000000000000000","transactions":[],"vbavailable":{},"vbrequired":0,"version":536870912});
    let output = gbt_to_job("miEandrT5UAoZHZU6wAh9bB99dETDyrNGk", input);
    assert_eq!(1,2);
}

#[test]
fn merklebranch_test() {
    let hashes: Vec<&str> = vec![
		"666dceaf1a6a90786651028248decd08435ed8d8486e304846998ecfe70a4f2e",
		"c29acd7e8c2b2ac21b0a2b1eeef8afda9451ce611328f6c34286dea129dd8759",
		"ac81ff8190271ea43b102074db5a908234855cbfd41133d3824d991b51c9f585",
		"2a42a2dac7b934748f28f97a96f2c035566e5a5ef4e19449afcf561a1989da20",
		"95375a84f3ced372b2b2f30d36323d11a42f4646fc0b716fa6c8f47e6ce73b34"];
	let hashes_deserial: Vec<Vec<u8>> = hashes.into_iter().map(|s: &str| {
        let mut v = Vec::from_hex(s).unwrap();
        v.reverse();
        v
    }).collect();
    let merkle = merklebranch(hashes_deserial);

    let mut res: Vec<Vec<u8>> = vec![
        "2e4f0ae7cf8e994648306e48d8d85e4308cdde488202516678906a1aafce6d66",
        "6c6f7308f08fdf16db8d23f49902ec980a91cfa5501a24e32e401a6be4a3de63",
        "e3fd33184df4154f75abb7e00e79633b957d2429f7b135a745d3e3fd1f3a0f86"]
            .into_iter().map(|s| Vec::from_hex(s).unwrap()).collect();
	assert_eq!(res, merkle)
}
