package curl

// GET-MODE (`-G` / `--get`) — pg2-b1lir W1, operator ruling 2026-10-27 (bead
// pg2-r9kv7).
//
// With -G, curl appends every -d/--data*/--data-urlencode value to the URL as a
// QUERY STRING and sends a GET (or HEAD under -I) instead of a POST. The legacy
// scan in effectiveMethod does not know that: it reads any body flag as an implied
// POST, so `curl -sG --data-urlencode 'query=up' http://127.0.0.1:9090/api/v1/query`
// (the Prometheus read shape that was ~77 pool denials, bead pg2-b1lir) abstained.
//
// THE RULING: honor -G so --data-urlencode is a query parameter, STILL loopback or
// .localhost only, and STILL deny -X non-GET, -T, -F, -K and -o outside the write
// roots. The legacy path is untouched (strictly additive widening): every
// invocation the strict walk below cannot fully understand falls back to it.
//
// THE WALK IS STRICT AND FAILS CLOSED. A flag token's VALUE must not be mistaken
// for a flag (`-H -G` carries the header "-G", it does not switch -G on; reading
// it as -G would turn a real POST into an approved "query"), so the walk consumes
// each value-taking option's operand using curl's own option table (curl 8.7.1
// `curl --help all`). A flag the table does not name, `--next` (which restarts
// option parsing), or a malformed operand makes the whole walk fail and the
// invocation is NOT approved by the get-mode path.
//
// THE FLAG TABLES ARE GENERATED from `curl --help all` of curl 8.7.1 (the entries
// with an `<arg>` operand are value-taking). A curl option this table does not
// list is rejected, never guessed at.

// curlLongValueFlags are the long options that take an operand.
var curlLongValueFlags = setOf(
	"--abstract-unix-socket", "--alt-svc", "--aws-sigv4", "--cacert", "--capath", "--cert",
	"--cert-type", "--ciphers", "--config", "--connect-timeout", "--connect-to",
	"--continue-at", "--cookie", "--cookie-jar", "--create-file-mode", "--crlfile", "--curves",
	"--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode",
	"--delegation", "--dns-interface", "--dns-ipv4-addr", "--dns-ipv6-addr", "--dns-servers",
	"--doh-url", "--dump-header", "--egd-file", "--engine", "--etag-compare", "--etag-save",
	"--expect100-timeout", "--form", "--form-string", "--ftp-account",
	"--ftp-alternative-to-user", "--ftp-method", "--ftp-port", "--ftp-ssl-ccc-mode",
	"--happy-eyeballs-timeout-ms", "--haproxy-clientip", "--header", "--help", "--hostpubmd5",
	"--hostpubsha256", "--hsts", "--interface", "--ipfs-gateway", "--json", "--keepalive-time",
	"--key", "--key-type", "--krb", "--libcurl", "--limit-rate", "--local-port",
	"--login-options", "--mail-auth", "--mail-from", "--mail-rcpt", "--max-filesize",
	"--max-redirs", "--max-time", "--netrc-file", "--noproxy", "--oauth2-bearer", "--output",
	"--output-dir", "--parallel-max", "--pass", "--pinnedpubkey", "--preproxy", "--proto",
	"--proto-default", "--proto-redir", "--proxy", "--proxy-cacert", "--proxy-capath",
	"--proxy-cert", "--proxy-cert-type", "--proxy-ciphers", "--proxy-crlfile",
	"--proxy-header", "--proxy-key", "--proxy-key-type", "--proxy-pass",
	"--proxy-pinnedpubkey", "--proxy-service-name", "--proxy-tls13-ciphers",
	"--proxy-tlsauthtype", "--proxy-tlspassword", "--proxy-tlsuser", "--proxy-user",
	"--proxy1.0", "--pubkey", "--quote", "--random-file", "--range", "--rate", "--referer",
	"--request", "--request-target", "--resolve", "--retry", "--retry-delay",
	"--retry-max-time", "--sasl-authzid", "--service-name", "--socks4", "--socks4a",
	"--socks5", "--socks5-gssapi-service", "--socks5-hostname", "--speed-limit",
	"--speed-time", "--stderr", "--telnet-option", "--tftp-blksize", "--time-cond",
	"--tls-max", "--tls13-ciphers", "--tlsauthtype", "--tlspassword", "--tlsuser", "--trace",
	"--trace-ascii", "--trace-config", "--unix-socket", "--upload-file", "--url",
	"--url-query", "--user", "--user-agent", "--variable", "--write-out",
)

// curlLongBoolFlags are the long options that take no operand.
var curlLongBoolFlags = setOf(
	"--anyauth", "--append", "--basic", "--ca-native", "--cert-status", "--compressed",
	"--compressed-ssh", "--create-dirs", "--crlf", "--digest", "--disable", "--disable-eprt",
	"--disable-epsv", "--disallow-username-in-url", "--doh-cert-status", "--doh-insecure",
	"--fail", "--fail-early", "--fail-with-body", "--false-start", "--form-escape",
	"--ftp-create-dirs", "--ftp-pasv", "--ftp-pret", "--ftp-skip-pasv-ip", "--ftp-ssl-ccc",
	"--ftp-ssl-control", "--get", "--globoff", "--haproxy-protocol", "--head", "--http0.9",
	"--http1.0", "--http1.1", "--http2", "--http2-prior-knowledge", "--http3", "--http3-only",
	"--ignore-content-length", "--include", "--insecure", "--ipv4", "--ipv6",
	"--junk-session-cookies", "--list-only", "--location", "--location-trusted",
	"--mail-rcpt-allowfails", "--manual", "--metalink", "--negotiate", "--netrc",
	"--netrc-optional", "--no-alpn", "--no-buffer", "--no-clobber", "--no-keepalive",
	"--no-npn", "--no-progress-meter", "--no-sessionid", "--ntlm", "--ntlm-wb", "--parallel",
	"--parallel-immediate", "--path-as-is", "--post301", "--post302", "--post303",
	"--proxy-anyauth", "--proxy-basic", "--proxy-ca-native", "--proxy-digest", "--proxy-http2",
	"--proxy-insecure", "--proxy-negotiate", "--proxy-ntlm", "--proxy-ssl-allow-beast",
	"--proxy-ssl-auto-client-cert", "--proxy-tlsv1", "--proxytunnel", "--raw",
	"--remote-header-name", "--remote-name", "--remote-name-all", "--remote-time",
	"--remove-on-error", "--retry-all-errors", "--retry-connrefused", "--sasl-ir",
	"--show-error", "--silent", "--socks5-basic", "--socks5-gssapi", "--socks5-gssapi-nec",
	"--ssl", "--ssl-allow-beast", "--ssl-auto-client-cert", "--ssl-no-revoke", "--ssl-reqd",
	"--ssl-revoke-best-effort", "--sslv2", "--sslv3", "--styled-output",
	"--suppress-connect-headers", "--tcp-fastopen", "--tcp-nodelay", "--tftp-no-options",
	"--tlsv1", "--tlsv1.0", "--tlsv1.1", "--tlsv1.2", "--tlsv1.3", "--tr-encoding",
	"--trace-ids", "--trace-time", "--use-ascii", "--verbose", "--version", "--xattr",
)
