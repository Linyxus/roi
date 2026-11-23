// Service worker: shows push notifications and opens the agent on tap.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data.json(); } catch (_) {}
  const note = d.kind === 'note';
  const title = note ? (d.title || 'roi') : d.kind === 'need' ? `${d.title || 'An agent'} needs you` : `${d.title || 'An agent'} finished`;
  e.waitUntil(self.registration.showNotification(title, {
    body: d.body || '',
    tag: note ? undefined : (d.agent || 'roi'),
    data: { url: d.url || (d.agent ? `/#/a/${d.agent}` : '/') },
    icon: '/icon-180.png',
  }));
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = e.notification.data.url;
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
    if (/^https?:/.test(url)) return self.clients.openWindow(url); // an outside link, such as a sign-in page
    for (const c of list) {
      if ('focus' in c) { c.navigate(url); return c.focus(); }
    }
    return self.clients.openWindow(url);
  }));
});
