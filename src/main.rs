extern crate reqwest;
#[macro_use] extern crate serde_json;
extern crate hyper;
extern crate base64;
extern crate hex;
extern crate crypto;

use reqwest::header::{Headers, UserAgent, Authorization, ContentType};
use hex::{FromHex, ToHex};
use crypto::digest::Digest;
use crypto::sha2::Sha256;


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
        return resp.json();
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
    let mut hasher = crypto::sha2::Sha256::new();
    let empty_vec: Vec<u8> = vec![0];

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
                //hasher.input(buf.as_slice());
                //let res = hasher.result();
                //hasher.input(res.as_slice());
                //new.push(hasher.result().to_vec());
            }
        }
        println!("arr {:?}; len {}", new, new.len());
        walker = new;
    }
    branch
}

fn main() {
    //let mut con = RpcConnection::new("http://127.0.0.1:20001", "admin1", "123");
    //let result = con.cmd("getblocktemplate", ()).unwrap();
	
    //println!("{:?}", result);
}

#[test]
fn reverse_test() {
	let res = reverse_8byte("ce322646a85ff6afbda7e43a3129a440d40600a6628890c1d524a0e5987045eb");
	let as_string = std::str::from_utf8(res.as_slice()).unwrap();
	assert_eq!("987045ebd524a0e5628890c1d40600a63129a440bda7e43aa85ff6afce322646", as_string)
}

#[test]
fn sha256d_test() {
    let o = sha256d(Vec::from("test"));
    println!("{:?}", o);
    assert_eq!(o.as_slice().to_hex(), "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08");
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
