// The daemon's event stream (server-sent events). A client compares versions
// only for inequality and reads the state again on any event, so this module
// only says "something may have changed" and whether the connection is up.
//
// A browser reconnects an EventSource by itself after a network error, but not
// after a refusal such as 503 not_ready while the daemon is starting, which
// closes it for good; so a closed stream is reopened after a pause.

/**
 * @param {object} o
 * @param {new (url: string) => EventSource} o.EventSource
 * @param {string} o.url
 * @param {() => void} o.onChange a state or store event arrived
 * @param {() => void} o.onOpen the stream is up
 * @param {() => void} o.onLost the stream broke
 * @param {typeof setTimeout} [o.setTimeout]
 * @param {number} [o.reopenMs]
 * @returns {() => void} closes the stream for good
 */
export function openStream({ EventSource, url, onChange, onOpen, onLost, setTimeout: later = globalThis.setTimeout, reopenMs = 3000 }) {
  let es = null;
  let stopped = false;
  let timer = null;

  function connect() {
    es = new EventSource(url);
    es.addEventListener("open", () => onOpen());
    es.addEventListener("state", () => onChange());
    es.addEventListener("store", () => onChange());
    es.addEventListener("error", () => {
      onLost();
      // readyState 2 is CLOSED: the browser will not retry, so the page does.
      if (!stopped && es.readyState === 2 && timer === null) {
        timer = later(() => {
          timer = null;
          if (!stopped) connect();
        }, reopenMs);
      }
    });
  }
  connect();
  return () => {
    stopped = true;
    es?.close();
  };
}
