// Only launch controls load this module. Their layout stays synchronous
// in app.js; copying and opening are wired before enabling the button.
export function wire(b, a, direct, solo, { t, api, status, statusTErr, copy }) {
  b.title = direct ? t("Open") + " " + a.name : (a.typed && !solo
    ? t("Type {command} in a new terminal to start {name} on magpie · click to copy the command for a terminal open now:", { command: a.typed, name: a.name })
    : t("{name} takes magpie only from its environment · click to copy the command that starts it:", { name: a.name })) + "\n" + a.launch;
  b.setAttribute("aria-label", b.title);
  b.disabled = false;
  b.onclick = (ev) => {
    ev.stopPropagation();
    if (!direct) {
      copy(a.launch, t("Launch command"), null, t("Copied — run it to start {name} on magpie", { name: a.name }));
      return;
    }
    b.disabled = true;
    api("agents/open", { agent: a.id }).then(() => status(t("Opened"))).catch(statusTErr).finally(() => { b.disabled = false; });
  };
}
