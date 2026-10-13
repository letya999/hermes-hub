# Live phase 1 capability checks

1. Verify terminal, code execution, files, web extraction and skills against the running dev Docker stack.
2. Repair the observed CLI exit-code diagnostic and the overly broad legacy runtime `/scope` mount.
3. Add regression coverage for both contracts and stabilize the observed companion startup timeout.
4. Run `just check`, rebuild affected services through hubctl, and repeat the live checks where deployment permits.
