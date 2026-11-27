// Settings edits the explicit Magpie-wide npm upstream; Plugins also
// shows its effective registry and offers to change the same setting.
(() => {
  let box = null, mine = null, registryChanged = null;
  let editing = false, draft = "", err = "", saving = false;

  let saved, pending = false;
  window.renderNPMRegistry = (s) => {
    const field = $("#npmRegistryInput"), save = $("#npmRegistrySave"), error = $("#npmRegistryError");
    const was = saved;
    saved = s.npmRegistry || "";
    const clearError = () => { error.hidden = true; error.textContent = ""; field.removeAttribute("aria-invalid"); };
    if (!pending && (was === undefined || field.value.trim() === was)) {
      field.value = saved;
      clearError();
    }
    const buttons = () => { save.disabled = pending || field.value.trim() === saved; };
    field.disabled = pending;
    buttons();
    field.oninput = () => { clearError(); buttons(); };
    save.onclick = async () => {
      if (save.disabled) return;
      const npmRegistry = field.value.trim();
      const focused = field === document.activeElement || save === document.activeElement;
      clearError();
      pending = field.disabled = true;
      buttons();
      const failed = await savePrefs({ npmRegistry: npmRegistry });
      if (failed) {
        error.textContent = t(failed.message);
        error.hidden = false;
        field.setAttribute("aria-invalid", "true");
      } else {
        saved = prefs.npmRegistry || "";
        field.value = saved;
        if (view === "plugins") window.loadPlugins?.();
        window.libraryNPMRegistryChanged?.();
      }
      pending = field.disabled = false;
      buttons();
      if (focused && document.activeElement === document.body && view === "settings") field.focus(NO_SCROLL);
    };
    field.onkeydown = (e) => {
      e.stopPropagation();
      if (e.isComposing) return;
      if (e.key === "Enter") save.click();
      else if (e.key === "Escape") { field.value = saved; field.oninput(); }
    };
  };

  function from(r) {
    switch (r.from) {
      case "magpie": return t("Settings › Network and sharing");
      case "env": return t("from {name}", { name: r.var });
      case "shell": return t("from your shell's {name}", { name: r.var });
      case "bunfig": case "npmrc": return t("from {name}", { name: r.path });
      case "china": return t("Mirrors in China");
    }
    return t("npm's own");
  }

  function button(label, cls, fn) {
    const b = el("button", cls, label);
    b.type = "button";
    b.disabled = saving;
    b.onclick = fn;
    return b;
  }

  async function set(url) {
    if (saving) return;
    saving = true;
    draw();
    try {
      const { registry } = await writingPrefs(api("plugins/registry", { url }));
      editing = false;
      draft = err = "";
      registryChanged(registry);
      if (prefs) {
        prefs.npmRegistry = registry.from === "magpie" ? registry.url : "";
        if (view === "settings") window.renderNPMRegistry(prefs);
      }
      window.libraryNPMRegistryChanged?.();
    } catch (e) {
      err = t(e.message);
      statusTErr(e);
    }
    saving = false;
    draw();
  }

  function draw() {
    const r = mine.registry;
    box.replaceChildren(el("span", "pm-mlabel", t("Magpie-wide npm registry")));
    if (!editing) {
      const change = button(t("Change"), "text", () => { editing = true; draft = r.from === "magpie" ? r.url : ""; draw(); });
      change.title = t("Settings › Network and sharing");
      box.append(el("code", "pm-reg-url", r.url), el("span", "pm-reg-from", from(r)), el("span", "grow"), change);
      if (r.from === "magpie") {
        const def = button(t("Use default"), "text", () => set(""));
        def.title = t("Clear Magpie's npm registry and restore automatic selection");
        box.append(def);
      }
      return;
    }
    const i = el("input", "pm-minput pm-reg-input");
    i.value = draft;
    i.placeholder = "https://registry.npmmirror.com";
    i.spellcheck = false;
    i.autocomplete = "off";
    i.setAttribute("aria-label", t("Magpie-wide npm registry"));
    i.oninput = () => { draft = i.value; };
    const save = button(t("Save"), "text primary", () => {
      const v = i.value.trim();
      if (v) set(v); else i.focus(NO_SCROLL);
    });
    const cancel = button(t("Cancel"), "text", () => { editing = false; draft = err = ""; draw(); });
    cancel.disabled = false;
    i.onkeydown = (ev) => { ev.stopPropagation(); if (ev.key === "Enter") save.onclick(); else if (ev.key === "Escape") cancel.onclick(); };
    box.append(i, cancel, save);
    if (err) box.append(el("div", "pm-reg-err", err));
    i.focus(NO_SCROLL);
  }

  // plugins.js calls this at each redraw; an open field is left as it is
  window.pmRegistry = (b, m, changed) => {
    const fresh = box !== b;
    box = b; mine = m; registryChanged = changed;
    if (fresh || !editing) draw();
  };
})();
