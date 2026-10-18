# CHG-0115: Routine snooze acceptance correction

User acceptance on 2026-10-18 showed a no-op snooze reported as a moved run.
The next recurrence was the following morning, while the requested deadline
was earlier the same day. This change enforces the existing SPEC-0056 rule
that snooze defers the next occurrence.

1. Reproduce the HTTP no-op with a regression test, then reject a deadline
   that is not later than the effective next run without changing revision.
2. Check the user's retired routine cards and occurrence history against the
   pasted transcript, including the claimed 10:31 run.
3. Rebuild the communication dev image through hubctl; test the live HTTP
   rejection, one real Telegram reminder, its delivery evidence and cleanup.
4. Run just check and document remaining limits of this acceptance.

No upstream patch, dependency, production rollout or unrelated routine edit.
