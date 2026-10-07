#!/usr/bin/env python3
"""Vendor the site's/admin's third-party runtime assets (no CDN at runtime).

Downloads pinned upstream releases, verifies their integrity hashes and writes
identical copies into web/static and admin/static:

  js/htmx.min.js                    htmx.org@1.9.12 (0BSD), SRI-checked
  fonts/manrope-latin.woff2         @fontsource-variable/manrope (OFL-1.1),
  fonts/manrope-cyrillic.woff2        variable wght 200-800, Google Fonts
  fonts/manrope-cyrillic-ext.woff2    subsets
  fonts/inter-kyrgyz.woff2          @fontsource-variable/inter (OFL-1.1)
                                      cyrillic-ext, subset to U+04A2-04A3:
                                      Manrope has no Ң/ң (it does have Ө ө Ү ү)
  fonts/LICENSE-manrope.txt, fonts/LICENSE-inter.txt

The @font-face rules live inline in web/templates/layout.gohtml and
admin/templates/layout.gohtml (URLs go through the "asset" func, so the
?v= hashes update by themselves). Tests: internal/web/selfhost_assets_test.go.

Run from anywhere:

  pip install --user fonttools brotli
  python3 scripts/vendor-web-assets.py
"""

import base64
import hashlib
import io
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
STATIC_DIRS = [ROOT / "web/static", ROOT / "admin/static"]

HTMX_URL = "https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js"
HTMX_SRI = "sha384-ujb1lZYygJmzgSwoxRggbCHcjc0rB2XoQrxeTUQyRjrOnlCoYta87iKBWq3EsdM2"

NPM = "https://registry.npmjs.org"
MANROPE = ("@fontsource-variable/manrope", "5.3.0",
           "sha512-6D5dgokHsWDDMtmXHznKa0hK229NN+1a4BLPmUCLqcO1Pw5EEhWY5RFt0AcXnVRAljFFPfRtLkJePQj6LSsV6g==")
INTER = ("@fontsource-variable/inter", "5.3.0",
         "sha512-OupL48va4JNofb97w6NYeF9S7W/kHNKM0Er8Dem5nqi4jeOLrVJDoE8tZEpnMJmtkvNbB1EIPPwHcdkF6b1oUA==")

MANROPE_SUBSETS = ["latin", "cyrillic", "cyrillic-ext"]
KYRGYZ_MISSING = "U+04A2-04A3"  # Ң ң — absent from Manrope


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as r:  # noqa: S310 - fixed https URLs
        return r.read()


def sri(data, algo):
    return f"{algo}-" + base64.b64encode(hashlib.new(algo, data).digest()).decode()


def npm_package(name, version, integrity):
    short = name.split("/")[-1]
    data = fetch(f"{NPM}/{name}/-/{short}-{version}.tgz")
    got = sri(data, "sha512")
    if integrity and got != integrity:
        sys.exit(f"{name}@{version}: integrity {got}, want {integrity}")
    print(f"{name}@{version} {got}")
    tar = tarfile.open(fileobj=io.BytesIO(data), mode="r:gz")
    return lambda rel: tar.extractfile(f"package/{rel}").read()


def write_all(rel, data):
    for d in STATIC_DIRS:
        out = d / rel
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_bytes(data)


def main():
    htmx = fetch(HTMX_URL)
    if sri(htmx, "sha384") != HTMX_SRI:
        sys.exit(f"htmx integrity mismatch: {sri(htmx, 'sha384')}")
    write_all("js/htmx.min.js", htmx)

    manrope = npm_package(*MANROPE)
    for s in MANROPE_SUBSETS:
        write_all(f"fonts/manrope-{s}.woff2", manrope(f"files/manrope-{s}-wght-normal.woff2"))
    write_all("fonts/LICENSE-manrope.txt", manrope("LICENSE"))

    inter = npm_package(*INTER)
    with tempfile.TemporaryDirectory() as tmp:
        src = pathlib.Path(tmp) / "inter-cyrillic-ext.woff2"
        out = pathlib.Path(tmp) / "inter-kyrgyz.woff2"
        src.write_bytes(inter("files/inter-cyrillic-ext-wght-normal.woff2"))
        subprocess.run(
            [sys.executable, "-m", "fontTools.subset", str(src),
             f"--unicodes={KYRGYZ_MISSING}", "--flavor=woff2", "--no-hinting",
             "--layout-features=*", f"--output-file={out}"],
            check=True,
        )
        write_all("fonts/inter-kyrgyz.woff2", out.read_bytes())
    write_all("fonts/LICENSE-inter.txt", inter("LICENSE"))


if __name__ == "__main__":
    main()
