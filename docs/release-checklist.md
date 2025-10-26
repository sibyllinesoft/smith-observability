# Release Checklist

Use this guide when cutting a public release of the Smith Observability toolkit.

1. **Start from a clean tree**
   ```bash
   git status -sb
   ```
   Ensure only the release-related version bump commits are pending.

2. **Update the version**
   - Edit `package.json` / `package-lock.json`.
   - Note the change in `CHANGELOG.md` (if applicable).

3. **Rebuild the Bifrost image**
   ```bash
   docker compose -p smith-observability build bifrost
   ```
   This compiles Bifrost with the latest patches for the OTEL converter.

4. **Run the full test suite**
   ```bash
   npm test
   npm run test:e2e
   ```
   The E2E test spins the stack, runs `smith observe codex`, prints the ClickHouse queries it executes, and validates that tool call payloads land in `gen_ai.responses.output_json`.

5. **Smoke-test manually (optional but recommended)**
   ```bash
   OPENAI_API_KEY=test \
   SMITH_OBSERVABILITY_BIFROST_CONFIG=$(pwd)/test/support/bifrost-stub.config.json \
     node bin/smith.mjs observe codex -- \
       --model openai/gpt-4o-mini \
       exec "Call the \`list_directory\` tool on \".\" to list the repository root, then summarize what you found."
   ```
   Inspect ClickHouse for the captured payload if you want an extra sanity check.

6. **Publish**
   ```bash
   npm publish
   git tag vX.Y.Z
   git push origin main --tags
   ```

7. **Announce**
   Share the highlights (notably the Bifrost OTEL converter patch and the e2e demonstration) in the release notes or project changelog.
