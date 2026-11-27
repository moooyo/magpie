// the day in the reader's own calendar, which the daily pick turns on
function today() {
  const date = new Date();
  return Math.floor((date.getTime() - date.getTimezoneOffset() * 6e4) / 864e5);
}

function picks(listings, { usersOf, likesOf, hotOf, byHot, entryOf }) {
  const used = listings.filter((item) => usersOf(item) > 0).sort((left, right) =>
    usersOf(right) - usersOf(left) || likesOf(right) - likesOf(left) || byHot(left, right));
  const liked = listings.filter((item) => likesOf(item) > 0).sort((left, right) =>
    likesOf(right) - likesOf(left) || usersOf(right) - usersOf(left) || byHot(left, right));
  // with no counts from magpie (offline, or none yet), npm's downloads
  const downloaded = used.length ? [] : listings.filter((item) => !item.github && hotOf(item) > 0).sort(byHot);
  // the day's: one of the community's most used not installed yet, a
  // different one each day
  const eligible = (used.length ? used : downloaded).filter((item) => !item.github);
  if (!eligible.length) return null;
  let pool = eligible.filter((item) => item.community && !entryOf(item.package)).slice(0, 14);
  if (!pool.length) pool = eligible.slice(0, 14);
  return { day: pool[today() % pool.length], used: used.slice(0, 5), liked: liked.slice(0, 5), downloaded: downloaded.slice(0, 5) };
}

// Render the picks once the page has the lists and counts it needs. The
// page can draw its loading skeleton before this module arrives.
export function weekPicks(helpers) {
  const { el, t, usersOf, likesOf, hotOf, opens, logo, kindOf, short, byline, count, summary, detail, actionFor } = helpers;

  function hero(l) {
    const box = el("article", "pm-hero");
    opens(box, l);
    const top = el("div", "pm-htop");
    const who = el("div", "pm-who");
    const nm = el("div", "pm-name");
    nm.append(el("b", "", l.name));
    who.append(nm, byline(l));
    const lg = logo(l.icon || l.npm?.icon, true, kindOf(l));
    lg.classList.add("hero");
    top.append(lg, who);
    const stats = el("div", "pm-stats");
    const stat = (v, k) => { const s = el("div", "pm-stat"); s.append(el("b", "", v), el("span", "", k)); stats.append(s); };
    const u = usersOf(l);
    if (u) stat(count(u), t("Users"));
    if (hotOf(l) || !u) stat(count(hotOf(l)), t("Downloads last week"));
    stat(t(short[kindOf(l)]), t("Kind"));
    if (l.npm?.version) stat("v" + l.npm.version, t("Version"));
    const acts = el("div", "pm-hact");
    const more = el("button", "pm-act ghost", t("Details"));
    more.type = "button";
    more.onclick = (ev) => { ev.stopPropagation(); detail(l); };
    acts.append(actionFor(l.package, l.name, l), more);
    box.append(el("div", "pm-eyebrow", t("Today's pick")), top, el("p", "pm-hsum", summary(l)), stats, acts);
    return box;
  }

  function rankList(title, note, list, line) {
    const box = el("div", "pm-rank");
    const h = el("div", "pm-rhead");
    h.append(el("b", "", title), el("span", "", note));
    box.append(h);
    list.forEach((l, i) => {
      const row = el("div", "pm-rrow");
      opens(row, l);
      const who = el("div", "pm-rwho");
      who.append(el("b", "", l.name), el("span", "", line(l)));
      row.append(el("span", "pm-rnum", String(i + 1)), logo(l.icon || l.npm?.icon, false, kindOf(l)), who, actionFor(l.package, l.name, l));
      box.append(row);
    });
    return box;
  }

  return (all) => {
    const p = picks(all, helpers);
    if (!p) return null;
    const grid = el("div", "pm-rec");
    grid.append(hero(p.day));
    if (p.used.length) grid.append(rankList(t("Most used"), t("in magpie, last week"), p.used, (l) => t(short[kindOf(l)]) + " · " + t("{n} use it", { n: count(usersOf(l)) })));
    else grid.append(rankList(t("Most downloaded"), t("last week, on npm"), p.downloaded, (l) => t(short[kindOf(l)]) + " · ↓" + t("{n}/week", { n: count(hotOf(l)) })));
    if (p.liked.length) grid.append(rankList(t("Most liked"), t("liked in magpie"), p.liked, (l) => "♥ " + count(likesOf(l)) + " · " + t(short[kindOf(l)])));
    else grid.classList.add("two");
    return grid;
  };
}
