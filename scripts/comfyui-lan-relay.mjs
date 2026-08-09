import net from "node:net";

const [upstreamHost = "192.168.112.18", upstreamPortValue = "8188", listenPortValue = "48188", localAddress = ""] = process.argv.slice(2);
const upstreamPort = readPort(upstreamPortValue, "upstream port");
const listenPort = readPort(listenPortValue, "listen port");

const server = net.createServer((client) => {
    const upstream = net.createConnection({ host: upstreamHost, port: upstreamPort, ...(localAddress ? { localAddress } : {}) });
    const close = () => {
        client.destroy();
        upstream.destroy();
    };
    client.on("error", close);
    upstream.on("error", (error) => {
        console.error(`ComfyUI upstream ${upstreamHost}:${upstreamPort}: ${error.message}`);
        close();
    });
    client.pipe(upstream).pipe(client);
});

server.on("error", (error) => {
    console.error(error.message);
    process.exitCode = 1;
});
server.listen(listenPort, "127.0.0.1", () => {
    console.log(`ComfyUI relay: 127.0.0.1:${listenPort} -> ${upstreamHost}:${upstreamPort}`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
    process.on(signal, () => server.close(() => process.exit(0)));
}

function readPort(value, label) {
    const port = Number(value);
    if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(`Invalid ${label}: ${value}`);
    return port;
}
