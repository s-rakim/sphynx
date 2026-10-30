// Sphynx: every device on the network is its own contact, listed before it
// has opened the chat, and messages only go to devices with a browser.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chatAad, newMessageId } from '../web/crypto.js';
import { connectUser, startServer } from './helpers.js';

let server;

before(async () => {
  server = await startServer({ CHAT_DEV_DEVICES: 'phone:Android:Ana,laptop:Windows:Ana,tv:tvOS:' });
});

after(async () => {
  await server?.stop();
});

const byId = (list) => Object.fromEntries(list.map((u) => [u.id, u]));

test('devices on the network are contacts before they open the chat', async () => {
  const phone = await connectUser(server.base, 'phone');
  const users = byId((await phone.call('GET', '/api/users')).users);
  assert.deepEqual(Object.keys(users).sort(), ['laptop@dev', 'phone@dev', 'tv@dev']);
  assert.equal(users['laptop@dev'].name, 'Laptop');
  assert.equal(users['laptop@dev'].device, 'Laptop');
  assert.equal(users['laptop@dev'].os, 'Windows');
  assert.equal(users['laptop@dev'].owner, 'Ana');
  assert.equal(users['laptop@dev'].joined, false);
  assert.equal(users['laptop@dev'].onNetwork, true);
  assert.equal(users['phone@dev'].joined, true);
  assert.equal(users['phone@dev'].os, 'Android');
});

test('each device gets its own chat, and joins when it opens the chat', async () => {
  const phone = await connectUser(server.base, 'phone');
  await phone.socket();
  const { chat } = await phone.call('POST', '/api/chats/direct', { userId: 'tv@dev' });
  assert.equal(chat.type, 'direct');
  assert.deepEqual(chat.members.map((m) => m.userId).sort(), ['phone@dev', 'tv@dev']);
  // Nothing to encrypt for yet: the TV has no browser.
  const before = (await phone.call('GET', `/api/chats/${chat.id}/devices`)).devices;
  assert.ok(before.every((d) => d.userId === 'phone@dev'));

  const tv = await connectUser(server.base, 'tv');
  const joined = await phone.waitFor((e) => e.type === 'user' && e.user.id === 'tv@dev' && e.user.joined);
  assert.equal(joined.user.os, 'tvOS');
  const devices = (await phone.call('GET', `/api/chats/${chat.id}/devices`)).devices;
  assert.ok(devices.some((d) => d.userId === 'tv@dev'));

  const id = newMessageId();
  const payload = await phone.ring.seal({ type: 'text', text: 'movie night?' }, chatAad(chat.id, id), devices);
  await phone.call('POST', `/api/chats/${chat.id}/messages`, { id, payload });
  const { messages } = await tv.call('GET', `/api/chats/${chat.id}/messages`);
  const m = messages.find((x) => x.id === id);
  assert.equal((await tv.ring.open(m.payload, chatAad(chat.id, id), 'phone@dev')).text, 'movie night?');

  // A different device has a different chat with the phone.
  const laptop = await connectUser(server.base, 'laptop');
  const other = (await laptop.call('POST', '/api/chats/direct', { userId: 'phone@dev' })).chat;
  assert.notEqual(other.id, chat.id);
});

test('a chat name of its own keeps the device name visible', async () => {
  const laptop = await connectUser(server.base, 'laptop');
  await laptop.call('PATCH', '/api/me', { name: 'Ana at work' });
  const phone = await connectUser(server.base, 'phone');
  const users = byId((await phone.call('GET', '/api/users')).users);
  assert.equal(users['laptop@dev'].name, 'Ana at work');
  assert.equal(users['laptop@dev'].device, 'Laptop');
});

test('devices that are not on the network cannot be messaged', async () => {
  const phone = await connectUser(server.base, 'phone');
  await assert.rejects(phone.call('POST', '/api/chats/direct', { userId: 'stranger@dev' }), (e) => e.status === 404);
});
