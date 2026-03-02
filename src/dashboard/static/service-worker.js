// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
// Spiral Pool Dashboard — Service Worker
// Cache-first for static assets, network-first for API/HTML
const CACHE_VERSION = 'spiralpool-v1';
const STATIC_ASSETS = [
  '/static/css/cyberpunk.css',
  '/static/icons/icon.svg',
  '/static/manifest.json',
];

// Install: pre-cache static assets
self.addEventListener('install', event => {
  event.waitUntil(
    caches.open(CACHE_VERSION)
      .then(cache => cache.addAll(STATIC_ASSETS))
      .then(() => self.skipWaiting())
  );
});

// Activate: clean up old caches
self.addEventListener('activate', event => {
  event.waitUntil(
    caches.keys().then(keys =>
      Promise.all(keys.filter(k => k !== CACHE_VERSION).map(k => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

// Fetch: cache-first for static, network-first for everything else
self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);

  // Cache-first for static assets and fonts
  if (url.pathname.startsWith('/static/') || url.hostname === 'fonts.gstatic.com' || url.hostname === 'fonts.googleapis.com') {
    event.respondWith(
      caches.match(event.request).then(cached => {
        if (cached) return cached;
        return fetch(event.request).then(response => {
          if (response.ok) {
            const clone = response.clone();
            caches.open(CACHE_VERSION).then(cache => cache.put(event.request, clone));
          }
          return response;
        });
      })
    );
    return;
  }

  // Network-first for API calls and HTML pages
  event.respondWith(
    fetch(event.request).catch(() => caches.match(event.request))
  );
});
