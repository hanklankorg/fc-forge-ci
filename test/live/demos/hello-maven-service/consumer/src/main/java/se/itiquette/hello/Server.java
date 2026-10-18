package se.itiquette.hello;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;

/** Serves the greeting over HTTP. */
public final class Server {
  private Server() {}

  /** Returns the status and body for a request path. */
  static Response respond(String path) {
    return switch (path) {
      case "/" -> new Response(200, Greeter.greet());
      case "/health" -> new Response(200, "ok");
      default -> new Response(404, "not found");
    };
  }

  /** Starts the server on port 8080, or the port the PORT variable names. */
  public static void main(String[] args) throws IOException {
    int port = Integer.parseInt(System.getenv().getOrDefault("PORT", "8080"));
    HttpServer server = HttpServer.create(new InetSocketAddress(port), 0);
    server.createContext("/", Server::handle);
    server.start();
  }

  private static void handle(HttpExchange exchange) throws IOException {
    Response response = respond(exchange.getRequestURI().getPath());
    byte[] body = response.body().getBytes(StandardCharsets.UTF_8);
    exchange.getResponseHeaders().set("Content-Type", "text/plain; charset=utf-8");
    exchange.sendResponseHeaders(response.status(), body.length);
    try (OutputStream out = exchange.getResponseBody()) {
      out.write(body);
    }
  }

  /** An HTTP status and its plain-text body. */
  record Response(int status, String body) {}
}
