/* Progressive timetable enhancement. No network requests until Save changes. */
(() => {
  'use strict';
  const root = document.querySelector('[data-schedule]');
  if (!root) return;
  const editor = root.querySelector('[data-paint-editor]');
  const form = root.querySelector('[data-slot-save]');
  const saveButton = root.querySelector('[data-save-button]');
  const status = root.querySelector('[data-save-status]');
  const error = root.querySelector('[data-save-error]');
  const scroll = root.querySelector('[data-week-scroll]');
  const slots = [...root.querySelectorAll('[data-slot]')];
  const dirty = new Map();
  const symbols = { want: '✓', cant: '×', unknown: '·', mixed: '◐' };
  const labels = { want: 'want to play', cant: "can't play", unknown: 'not answered', mixed: 'saved partial slot' };
  let mode = 'want', posting = false, drag = null;
  const dayTabs = [...root.querySelectorAll('[data-day-tab]')];
  let selectedDay = dayTabs[0]?.dataset.dayTab;
  const returnKey = `playpile:timetable:${new URL(form.action, location.href).pathname}:${new URLSearchParams(new FormData(form)).get('tz')}`;
  const selectDay = date => {
    if (!dayTabs.some(button => button.dataset.dayTab === date)) return false;
    selectedDay = date;
    dayTabs.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.dayTab === date)));
    root.querySelectorAll('[data-day]').forEach(day => day.classList.toggle('selected-day', day.dataset.day === date));
    return true;
  };
  const update = () => {
    saveButton.disabled = posting || dirty.size === 0;
    status.textContent = posting ? 'Saving…' : dirty.size ? `Unsaved · ${dirty.size} changed slot${dirty.size === 1 ? '' : 's'}` : 'Saved · no changes';
  };
  const paint = slot => {
    if (!slot || slot.disabled || posting) return;
    slot.dataset.state = mode;
    slot.querySelector('.slot-symbol').textContent = symbols[mode];
    slot.setAttribute('aria-label', `${slot.dataset.label}: ${labels[mode]}`);
    slot.title = `${slot.dataset.label}: ${labels[mode]}`;
    if (slot.dataset.original === mode) dirty.delete(slot.dataset.slot);
    else dirty.set(slot.dataset.slot, { date: slot.closest('[data-day]').dataset.day, start: slot.dataset.slot, state: mode });
    update();
  };
  slots.forEach(slot => {
    slot.dataset.label = slot.title.replace(/: (unknown|want|cant|mixed)$/, '');
    slot.addEventListener('pointerdown', event => {
      if (posting || event.button !== 0) return;
      drag = { id: event.pointerId, touch: event.pointerType !== 'mouse', x: event.clientX, y: event.clientY, start: slot, last: slot };
      if (!drag.touch) { event.preventDefault(); paint(slot); }
    });
    // Native keyboard button activation produces detail=0; mouse/touch paint on pointer events.
    slot.addEventListener('click', event => { if (event.detail === 0) paint(slot); });
  });
  document.addEventListener('pointermove', event => {
    if (!drag || drag.id !== event.pointerId || drag.touch) return;
    event.preventDefault();
    const hit = document.elementFromPoint(event.clientX, event.clientY)?.closest('[data-slot]');
    if (!hit || hit === drag.last) return;
    // Paint every crossed row in this column, even when pointer events are sparse.
    if (hit.closest('[data-day]') === drag.last.closest('[data-day]')) {
      const column = [...hit.closest('[data-day]').querySelectorAll('[data-slot]')];
      const a = column.indexOf(drag.last), b = column.indexOf(hit);
      column.slice(Math.min(a,b), Math.max(a,b)+1).forEach(paint);
    } else paint(hit);
    drag.last = hit;
  }, { passive: false });
  document.addEventListener('pointerup', event => {
    if (!drag || drag.id !== event.pointerId) return;
    if (drag.touch && Math.hypot(event.clientX-drag.x, event.clientY-drag.y) < 10) paint(drag.start);
    drag = null;
  });
  document.addEventListener('pointercancel', () => { drag = null; });
  root.querySelectorAll('[data-mode]').forEach(button => button.addEventListener('click', () => {
    if (posting) return;
    mode = button.dataset.mode;
    root.querySelectorAll('[data-mode]').forEach(other => other.setAttribute('aria-pressed', String(other === button)));
  }));
  dayTabs.forEach(button => button.addEventListener('click', () => selectDay(button.dataset.dayTab)));
  root.querySelectorAll('[data-clear-day]').forEach(button => button.addEventListener('click', () => {
    if (posting) return;
    const previous = mode; mode = 'unknown';
    button.closest('[data-day]').querySelectorAll('[data-slot]').forEach(paint);
    mode = previous;
  }));
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (posting || !dirty.size) return;
    const days = new Map();
    dirty.forEach(change => {
      if (!days.has(change.date)) days.set(change.date, { date: change.date, changes: [] });
      days.get(change.date).changes.push({ start: change.start, state: change.state });
    });
    const data = new URLSearchParams(new FormData(form));
    data.set('edits', JSON.stringify([...days.values()]));
    posting = true; error.hidden = true; update();
    root.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(form.action, { method: 'POST', body: data, headers: { Accept: 'application/json' } });
      if (!response.ok || response.redirected) throw new Error('Your changes were not saved. Your paint is still here; check your connection or session, then try again.');
      const result = await response.json();
      if (!result.saved) throw new Error('Could not confirm the save. Your paint is still here; try again.');
      dirty.clear();
      slots.forEach(slot => { slot.dataset.original = slot.dataset.state; });
      // Consume this small viewport bookmark once; never store unsaved paint.
      try {
        window.sessionStorage.setItem(returnKey, JSON.stringify({ day: selectedDay, top: scroll.scrollTop, at: Date.now() }));
      } catch (_) { /* Storage may be disabled; a confirmed save still succeeds. */ }
      // Reload suggestions only after a confirmed save; there is no unsaved paint now.
      location.reload();
    } catch (failure) { error.textContent = failure.message; error.hidden = false; }
    finally { posting = false; root.removeAttribute('aria-busy'); update(); }
  });
  window.addEventListener('beforeunload', event => {
    if (dirty.size) { event.preventDefault(); event.returnValue = ''; }
  });
  root.addEventListener('submit', event => {
    if (event.target !== form && dirty.size && !window.confirm('Leave without saving your painted slots?')) event.preventDefault();
  });
  // A sibling control, never nested in a paint button. No requests or writes.
  const crewDialog = root.querySelector('[data-crew-dialog]');
  const crewNames = [...root.querySelectorAll('[data-crew-name]')].map(node => node.textContent);
  const crewLabels = { w: '✓ Want to play', c: "× Can't play", u: '· Not answered', m: '◐ Partly marked — not a full-slot answer' };
  const crewStates = { w: 'want', c: 'cant', u: 'unknown', m: 'mixed' };
  if (crewDialog && crewNames.length) {
    const list = root.querySelector('[data-crew-choices]');
    root.querySelectorAll('[data-crew-slot]').forEach(button => {
      const summary = button.title;
      button.addEventListener('pointerenter', () => {
        button.title = crewNames.map((name, i) => `${name}: ${crewLabels[button.dataset.crewStates[i]] || crewLabels.u}`).join('\n');
      });
      button.addEventListener('pointerleave', () => { button.title = summary; });
      button.addEventListener('click', () => {
        drag = null;
        if (typeof crewDialog.showModal !== 'function') {
          const saved = root.querySelector('[data-crew-saved]');
          saved.open = true; saved.scrollIntoView({ block: 'center' });
          return;
        }
        root.querySelector('[data-crew-time]').textContent = button.dataset.crewLabel;
        list.replaceChildren();
        crewNames.forEach((name, i) => {
          const code = button.dataset.crewStates[i];
          const item = document.createElement('li');
          const who = document.createElement('strong'); who.textContent = name;
          const answer = document.createElement('span');
          answer.dataset.crewState = crewStates[code] || 'unknown';
          answer.textContent = crewLabels[code] || crewLabels.u;
          item.append(who, answer); list.append(item);
        });
        crewDialog.showModal();
      });
    });
    root.querySelector('[data-crew-close]').addEventListener('click', () => crewDialog.close());
  }
  const zone = root.querySelector('[data-timezone]');
  const detected = root.querySelector('[data-detected-zone]');
  try {
    const name = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (name && name !== zone.value) {
      detected.hidden = false; detected.textContent = `Use my timezone (${name})`;
      detected.addEventListener('click', () => { zone.value = name; zone.form.requestSubmit(); });
    }
  } catch (_) { /* Keep UTC fallback. */ }
  root.querySelectorAll('[data-copy-link]').forEach(button => {
    if (!navigator.clipboard) return;
    button.hidden = false;
    button.addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(new URL(button.dataset.copyLink, location.href).href); button.textContent = 'Link copied'; }
      catch (_) { button.textContent = 'Use the session link to share'; }
    });
  });
  editor.hidden = false;
  root.querySelector('[data-native-editor]').hidden = true;
  const afternoon = slots.find(slot => slot.querySelector('.slot-time').textContent === '15:00');
  if (afternoon) {
    const header = afternoon.closest('[data-day]').querySelector('.day-heading');
    scroll.scrollTop += afternoon.getBoundingClientRect().top - scroll.getBoundingClientRect().top - header.getBoundingClientRect().height - 8;
  }
  try {
    const raw = window.sessionStorage.getItem(returnKey);
    window.sessionStorage.removeItem(returnKey);
    if (raw) {
      const saved = JSON.parse(raw);
      const age = Date.now() - saved.at;
      if (Number.isFinite(saved.top) && saved.top >= 0 && Number.isFinite(age) && age >= 0 && age < 60000 && selectDay(saved.day)) {
        scroll.scrollTop = Math.min(saved.top, Math.max(0, scroll.scrollHeight - scroll.clientHeight));
      }
    }
  } catch (_) { /* Bad/stale bookmarks and unavailable storage keep the default view. */ }
})();
