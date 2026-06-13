// tatitok design system — template base loader.
// One file, one line for a consumer to edit: point `base` at wherever this
// design system's compiled tree lives relative to THIS page.
(() => {
  const base = '../..';
  for (const p of ['styles.css']) {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = base + '/' + p;
    document.head.appendChild(l);
  }
  const s = document.createElement('script');
  s.src = base + '/_ds_bundle.js';
  s.onerror = () =>
    console.error(
      'ds-base.js: failed to load ' + s.src +
      ' — point the base line at the bound _ds/<folder> tree relative to this page ' +
      '(e.g. _ds/<folder> at the project root, ../_ds/<folder> one level down); ' +
      'in a fresh design system this can just mean the bundle is not compiled yet'
    );
  document.head.appendChild(s);
})();
