# Five sandbox UI concepts

Open `index.html` directly in a browser. No build, dependencies, account, or controller is required. Use the five tabs at the top to switch designs. You can also open a specific design with the fragments `#guided`, `#overview`, `#studio`, `#workbench`, and `#launcher`.

These are interactive design proposals. Every sandbox, lifecycle action, egress setting, and test cluster exists only in browser memory. Refresh or **Reset demo** restores the sample projects. The page has no API client, analytics, external assets, or network connections. Its content security policy denies connections and form submissions.

- **Guided** walks through project, tools and access, cluster, and review.
- **Overview** puts existing projects beside a new sandbox panel.
- **Access studio** emphasizes readable rules and an illustrative request checker.
- **Workbench** puts project, policy, and testing controls in three dark panes.
- **Launcher** starts with the repository and uses two setup panels with a compact review.

All five support automatic names with a manual override, suggested Go/Python/Rust/npm download presets, selected destinations or full public web or offline access, request method presets, exact or folder path matching, generated regex previews, and a one-node or three-node test cluster. Create a sandbox, pause/resume it, or open **Access & cluster** to change its settings. Custom names survive repository changes; **Use repository name** restores automatic naming.

The destination editor uses hostnames, readable request types, and plain paths. **Read only** means GET/HEAD, **Submit only** means POST, and **Read and write** means GET/HEAD/POST/PUT/PATCH/DELETE. Enter a plain folder path such as `/v1/items`. Folder matching includes the folder and its children automatically; exact paths treat regex metacharacters literally. Details show a generated regex, which is a design preview rather than the controller's existing prefix-policy format.

The local/private network restriction is fixed in every design and cannot be switched off. The request checker rejects IP literals, local hostname forms, non-HTTPS URLs, credentials, and nondefault ports. It only models additional destination rules. It does not resolve DNS or model registry/repository presets, redirects, inspection, or rebinding. These mockups do not implement or qualify network enforcement. Full public web egress, regex translation, and nested test clusters require separate backend design and capability checks before integration. Unsupported capabilities must fail closed. Offline provisioning also needs an explicit source and image workflow.

For an HTTP preview, from the repository root run:

```sh
python -m http.server 8765 --bind 127.0.0.1 --directory docs/ui-concepts
```

Then open `http://127.0.0.1:8765`.

Browser qualification serves the mockups on a disposable loopback server and never contacts a controller or external service:

```sh
PLAYWRIGHT_MODULE_PATH=/path/to/playwright \
  node scripts/qualify-ui-concepts.cjs
```

It requires an existing Playwright installation and Chromium. Override `CHROMIUM_PATH` if needed. Set `APC_UI_SCREENSHOT_DIR` to save desktop and mobile previews. It checks all five creation/edit flows, lifecycle actions, custom names, cluster configuration, mobile overflow, literal regex matching, request method denial, local-address denial, offline denial, reset, browser errors, and absence of external requests.
