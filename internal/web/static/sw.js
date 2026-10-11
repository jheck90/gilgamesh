// Installable shell only. Pages always come from the server, so a stale
// cache can never show something from a position you've moved back from.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));
