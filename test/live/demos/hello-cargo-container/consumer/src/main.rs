//! A tiny HTTP service: `GET /` answers with a greeting and `GET /health`
//! with `ok`, on port 8080. Only the standard library, so the image build
//! fetches no crate.

use std::io::{self, BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};

const ADDRESS: &str = "0.0.0.0:8080";

fn main() -> io::Result<()> {
    let listener = TcpListener::bind(ADDRESS)?;
    println!("listening on {ADDRESS}");

    for stream in listener.incoming() {
        match stream {
            Ok(stream) => {
                if let Err(err) = handle(stream) {
                    eprintln!("request failed: {err}");
                }
            }
            Err(err) => eprintln!("connection failed: {err}"),
        }
    }

    Ok(())
}

/// Says hello.
fn greet(who: &str) -> String {
    format!("Hello, {who}!\n")
}

/// Returns the status line and body for a request line such as
/// `GET /health HTTP/1.1`.
fn respond(request_line: &str) -> (&'static str, String) {
    let mut parts = request_line.split_whitespace();

    match (parts.next(), parts.next()) {
        (Some("GET"), Some("/")) => ("200 OK", greet("Forge CI")),
        (Some("GET"), Some("/health")) => ("200 OK", "ok\n".to_owned()),
        (Some("GET"), Some(_)) => ("404 Not Found", "not found\n".to_owned()),
        _ => ("405 Method Not Allowed", "method not allowed\n".to_owned()),
    }
}

/// Reads one request, its headers included, and writes the response.
fn handle(mut stream: TcpStream) -> io::Result<()> {
    let mut reader = BufReader::new(&stream);
    let mut request_line = String::new();
    reader.read_line(&mut request_line)?;

    let mut header = String::new();
    while reader.read_line(&mut header)? > 0 && header.trim_end() != "" {
        header.clear();
    }

    let (status, body) = respond(&request_line);
    write!(
        stream,
        "HTTP/1.1 {status}\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )?;
    stream.flush()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Read;
    use std::thread;

    #[test]
    fn greets_by_name() {
        assert_eq!(greet("you"), "Hello, you!\n");
    }

    #[test]
    fn answers_the_root_and_health_paths() {
        assert_eq!(
            respond("GET / HTTP/1.1\r\n"),
            ("200 OK", "Hello, Forge CI!\n".to_owned())
        );
        assert_eq!(
            respond("GET /health HTTP/1.1\r\n"),
            ("200 OK", "ok\n".to_owned())
        );
    }

    #[test]
    fn refuses_other_paths_and_methods() {
        assert_eq!(respond("GET /missing HTTP/1.1\r\n").0, "404 Not Found");
        assert_eq!(respond("POST / HTTP/1.1\r\n").0, "405 Method Not Allowed");
        assert_eq!(respond("").0, "405 Method Not Allowed");
    }

    #[test]
    fn serves_a_request_over_loopback() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let server = thread::spawn(move || handle(listener.accept().unwrap().0));

        let mut client = TcpStream::connect(address).unwrap();
        client
            .write_all(b"GET /health HTTP/1.1\r\nHost: localhost\r\n\r\n")
            .unwrap();
        let mut response = String::new();
        client.read_to_string(&mut response).unwrap();

        server.join().unwrap().unwrap();
        assert!(response.starts_with("HTTP/1.1 200 OK\r\n"), "{response}");
        assert!(response.ends_with("\r\n\r\nok\n"), "{response}");
    }
}
