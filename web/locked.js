// Locked page: ask colima-dashboard to open the signed-in link, then reload once this browser has the cookie.
"use strict";
const btn = document.getElementById("unlock");
const msg = document.getElementById("unlockmsg");

async function signedIn() {
  try {
    const r = await fetch("/session", { credentials: "same-origin" });
    return (await r.json()).signedIn === true;
  } catch { return false; }
}

btn.addEventListener("click", async () => {
  btn.disabled = true;
  try {
    const r = await fetch("/unlock", { method: "POST", credentials: "same-origin" });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    msg.textContent = "Opened a signed-in tab. This page reloads when it is signed in too.";
    for (let i = 0; i < 60; i++) {
      await new Promise((res) => setTimeout(res, 1000));
      if (await signedIn()) { location.replace("/"); return; }
    }
    msg.textContent = "Still locked here: the link opened in your default browser. Use that tab, or run colima-dashboard open.";
  } catch (e) {
    msg.textContent = `Could not open it: ${e.message}. Run colima-dashboard open in a terminal.`;
  } finally {
    btn.disabled = false;
  }
});

// Already signed in (another tab finished first)? Go straight in.
signedIn().then((ok) => { if (ok) location.replace("/"); });
