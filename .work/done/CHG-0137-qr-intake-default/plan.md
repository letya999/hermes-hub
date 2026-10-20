# CHG-0137 Telegram QR intake by default, declared in the contract

Follow-on to CHG-0074. The QR login was opt-in behind a `telegramauth`
build tag and a second image; the flag lived in ignored `spaces/` settings
and silently reverted to the paste-a-session form.

- Compile QR into the one `communication-hub` binary; delete the tag,
  the disabled stub and the `*-telegram-auth` image targets.
- `telegram_auth` becomes tri-state: absent auto-enables on stacks with
  `ingress: [telegram]`, explicit `false` opts out.
- The contract declares `intake: telegram_qr`; ToolHub forwards it as
  `credential_intake` in the prepare notice instead of the gateway
  sniffing `ContractID == "telegram-session"`. Unknown intake values are
  rejected at contract admission.
- Broker form stays the fallback when intake is absent or QR state cannot
  initialize (gateway degrades, does not refuse to start).
