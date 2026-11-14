// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

'use strict';

const http = require('node:http');
const { greet } = require('./greeter.js');

// Returns the status and body for a request path.
function respond(path) {
  switch (path) {
    case '/':
      return { status: 200, body: greet() };
    case '/health':
      return { status: 200, body: 'ok' };
    default:
      return { status: 404, body: 'not found' };
  }
}

function handle(request, response) {
  const { status, body } = respond(new URL(request.url, 'http://localhost').pathname);
  response.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8' });
  response.end(body);
}

// Serves on port 8080, or the port the PORT variable names.
if (require.main === module) {
  const port = Number.parseInt(process.env.PORT ?? '8080', 10);
  const server = http.createServer(handle).listen(port);
  process.on('SIGTERM', () => server.close());
}

module.exports = { respond };
