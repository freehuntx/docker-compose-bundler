const http = require('http');

const server = http.createServer((req, res) => {
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end('Hello from bundled Docker Compose stack!\n');
});

const port = process.env.PORT || 80;
server.listen(port, () => {
  console.log(`Server running on port ${port}`);
});