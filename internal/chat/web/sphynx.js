// What Sphynx adds to the Tailchat app: every device on the Sphynx network
// is its own contact, and the app also runs on plain http addresses, where
// browsers hold back calls, the microphone, location and notifications.

// Where the chat can be opened (from /api/me), filled in at boot.
export const net = { address: '', secureAddress: '' };

export const secure = globalThis.isSecureContext === true;
export const canUseMedia = secure && !!globalThis.navigator?.mediaDevices?.getUserMedia;
export const canNotify = secure && typeof Notification !== 'undefined';
export const address = () => net.address || `${location.origin}/`;

// "Browsers only allow calls on secure (HTTPS) addresses. Open … instead."
export function needsHttps(what) {
  const next = net.secureAddress
    ? `Open ${net.secureAddress} to use them.`
    : 'Ask whoever runs Sphynx to turn on the HTTPS address under Chat in the Sphynx admin page.';
  return `Browsers only allow ${what} on secure (HTTPS) addresses. ${next}`;
}

// The line under a device's name: its name on the network when it picked
// a chat name of its own, then what it runs and whose it is.
export function deviceLine(u) {
  if (!u) return '';
  if (u.onNetwork === false) return 'No longer on your Sphynx network';
  if (u.joined === false) return "Hasn't opened Sphynx Chat yet";
  const parts = [];
  if (u.device && u.device !== u.name) parts.push(u.device);
  if (u.os) parts.push(u.os);
  if (u.owner) parts.push(u.owner);
  return parts.join(' · ') || 'Device on your Sphynx network';
}

// Messages are encrypted for each device's browsers; a device without one
// can't receive anything yet.
export const reachable = (u) => !!u && u.joined !== false && u.onNetwork !== false;

export function copyText(text, done, toast) {
  const ok = () => toast?.(done);
  if (navigator.clipboard && secure) {
    navigator.clipboard.writeText(text).then(ok, () => legacyCopy(text) && ok());
    return;
  }
  if (legacyCopy(text)) ok();
  else toast?.("Couldn't copy. Select the text and copy it instead.");
}

function legacyCopy(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
  document.body.append(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  return ok;
}

// A browser opening the http address for the first time goes to the HTTPS
// address instead, when there is one and it leads here through the tailnet
// (each address keeps its own keys, so this only happens before any were
// made). ?stay keeps it on http.
export async function preferSecure(hasKeys) {
  if (secure || hasKeys || !net.secureAddress) return false;
  try {
    if (new URLSearchParams(location.search).has('stay')) sessionStorage.setItem('sphynx-chat-stay', '1');
    if (sessionStorage.getItem('sphynx-chat-stay')) return false;
  } catch {
    // storage blocked: just check
  }
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), 4000);
  try {
    const res = await fetch(new URL('api/reach', net.secureAddress), { signal: ctl.signal, credentials: 'omit', cache: 'no-store' });
    if (!(await res.json()).ok) return false;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
  location.replace(new URL(location.pathname.slice(1) + location.hash, net.secureAddress).href);
  return true;
}
