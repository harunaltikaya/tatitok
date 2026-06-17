# Third-Party Notices

tatitok itself is distributed under the MIT License (see [`LICENSE`](LICENSE)).
It also incorporates third-party material, each under its own license, credited
below.

---

## LiteLLM — model price & context-window data

`internal/pricing/prices_snapshot.json` is a pinned, embedded snapshot of
LiteLLM's `model_prices_and_context_window.json`. Provenance (commit, hash,
fetch date) is also recorded in `internal/pricing/snapshot_meta.json`.

- **Project:** LiteLLM (BerriAI/litellm)
- **Source:** <https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json>
- **Snapshot commit:** `3b40ac987fb4fe08061b67dda91b286dc41bee28`
- **Snapshot SHA-256:** `3ceb8bff5ba6e98c074fb4b459a986b7d5d7f6fd983c2c5a0f3bd039cfc8215c`
- **Fetched:** 2026-06-11
- **License:** MIT

LiteLLM uses a dual-licensing structure: content under its `enterprise/`
directory is governed by a separate license, while everything else — including
the price-and-context-window data snapshotted here — is under the MIT License.
The repository's `LICENSE` reads, verbatim:

```
Portions of this software are licensed as follows:

* All content that resides under the "enterprise/" directory of this repository, if that directory exists, is licensed under the license defined in "enterprise/LICENSE".
* Content outside of the above mentioned directories or restrictions above is available under the MIT license as defined below.

MIT License

Copyright (c) 2023 Berri AI

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

---

## Jost — font

The dashboard self-hosts the Jost variable webfont (`web/src/fonts/jost-var.woff2`,
fingerprinted into the embedded build output). The complete license text travels
with the font in [`web/src/fonts/OFL.txt`](web/src/fonts/OFL.txt), as the OFL
requires.

- **Family:** Jost
- **Designer:** Owen Earl
- **Upstream:** <https://github.com/indestructible-type/Jost>
- **License:** SIL Open Font License, Version 1.1
- **Copyright:** Copyright 2020 The Jost Project Authors
  (<https://github.com/indestructible-type/Jost>)

---

## Bundled JavaScript dependencies

The dashboard bundle additionally vendors its npm dependencies (e.g. React and
Apache ECharts) into the embedded JavaScript. Their pinned versions and
integrity hashes are recorded in `web/package-lock.json`, and each retains its
own upstream license.
