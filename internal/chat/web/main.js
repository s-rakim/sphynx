// Entry point: unlock this device's keys, then draw the app.
import { boot } from './app.js';
import { mount } from './views.js';

const status = document.getElementById('boot-status');
boot((text) => {
  status.textContent = text;
}).then(mount, (err) => {
  console.error(err);
  status.textContent = err.message || 'Sphynx Chat could not start';
  if (err.link) {
    // e.g. the admin page to sign in to first, on the PC that runs Sphynx
    const a = document.createElement('a');
    a.href = err.link;
    a.className = 'btn primary';
    a.textContent = err.linkText || err.link;
    status.after(a);
  }
});
