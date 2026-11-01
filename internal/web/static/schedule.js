/* Scheduling enhancements only; all forms work without JavaScript. */
(() => {
  const root = document.querySelector('[data-schedule]');
  if (!root) return;
  const zone = root.querySelector('[data-timezone]');
  const detected = root.querySelector('[data-detected-zone]');
  try {
    const name = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (name && name !== zone.value) {
      detected.hidden = false;
      detected.textContent = `Use my timezone (${name})`;
      detected.addEventListener('click', () => { zone.value = name; zone.form.requestSubmit(); });
    }
  } catch (_) { /* Keep the usable server-rendered UTC fallback. */ }
  root.querySelectorAll('[data-copy-link]').forEach(button => {
    if (!navigator.clipboard) return;
    button.hidden = false;
    button.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(new URL(button.dataset.copyLink, location.href).href);
        button.textContent = 'Link copied';
      } catch (_) { button.textContent = 'Use the session link to share'; }
    });
  });
})();
