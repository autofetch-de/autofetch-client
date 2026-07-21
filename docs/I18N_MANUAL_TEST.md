# Manual test plan: German and English client builds

Run the complete test once with a `de` artifact and once with the matching `en` artifact. Use a clean configuration directory for the fresh-install cases and a copy of a real existing configuration for the upgrade case.

## 1. Artifact and metadata

- Confirm the filename contains the correct `de` or `en` suffix.
- Run `--version`.
- Verify version, commit, build date, platform, architecture, variant, and `language de|en`.
- Run `--help` and verify that user-facing option descriptions use the artifact language.
- Confirm option names, environment variables, paths, and technical identifiers remain unchanged.

## 2. Fresh pairing

- Start without `client.json`.
- Verify the pairing instructions use the artifact language.
- Copy the code and open the pairing page.
- Approve the client and confirm that credentials are saved.
- Repeat with an expired code and a rejected code.
- Test an unreachable server and a connection timeout.
- Confirm `--print-code-only` outputs only the code.

## 3. Graphical client and system tray

- Check the initial status window, pairing card, buttons, log section, and status bar.
- Check the entire system tray menu.
- Start, pause, resume, minimize to tray, restore, and quit.
- Confirm no text from the other language appears.
- Confirm technical log lines remain English in both builds.

## 4. General settings

- Open settings and verify every label, hint, button, and confirmation.
- Select and save a valid download folder.
- Test a missing folder and a folder without write permission.
- Restart and confirm existing settings remain intact.

## 5. IRC settings

- Check network list, global settings, network details, placeholders, and buttons.
- Save NickServ and SASL credentials.
- Delete saved credentials.
- Enable and disable Reverse DCC.
- Verify port-range explanations and validation errors.
- Confirm IRC commands, channel names, hosts, paths, and protocol terms are not translated.

## 6. Local browser interface

- Start the non-headless command-line client with the local interface enabled.
- Open `/`, `/settings`, and `/irc/setup`.
- Verify `<html lang="de">` or `<html lang="en">`.
- Test start, stop, connection test, re-pair, code copy, and log copy.
- Confirm JavaScript notices use the artifact language.
- Confirm the interface uses no cookies, local storage, analytics, or external translation service.

## 7. Downloads and visible errors

Test at least:

- queued and active download
- successful completion
- canceled or paused download
- HTTP 404 and another HTTP failure
- missing or invalid download information
- IRC connection failure
- required registered nick
- SASL or NickServ failure
- XDCC offer timeout
- incomplete XDCC transfer
- filename mismatch
- Reverse DCC disabled
- Reverse DCC port forwarding required

For each case verify:

- the visible explanation is natural in the selected language
- no translation key is displayed
- the technical code is unchanged
- the technical log remains useful and English

## 8. Upgrade compatibility

- Start the new German artifact with an existing pre-i18n `client.json` and `irc-secrets.json`.
- Confirm no pairing, download, IRC, or secret setting is lost.
- Replace the binary with the English artifact while keeping the same configuration.
- Confirm the UI changes to English without altering configuration data.
- Replace it again with the German artifact and repeat the check.

## 9. Platform matrix

Headless:

- Linux AMD64
- Linux ARM64
- Linux ARMv7
- Windows AMD64
- Windows 386
- macOS Intel
- macOS Apple Silicon

GUI:

- Linux AMD64
- Windows AMD64
- macOS Intel
- macOS Apple Silicon

For every release artifact, perform at least the metadata check, first start, pairing, restart, and clean shutdown.
