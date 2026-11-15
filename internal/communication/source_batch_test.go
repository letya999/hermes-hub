package communication

// The durable-batch machinery this file drove (streamPollOnce, PendingBatch,
// event-state cursor admission, agent-sink remainder groups) moved to
// internal/toolhub/automation's eventStore + dispatchQueue. Every invariant
// below is now asserted against the real owner seams in
// internal/toolhub/automation/streams_ported_test.go, host_test.go, and the
// internal/routines suite:
//
//   - TestBatchTenEventsEightSlotsRestartRemainder ->
//     TestStreamBatchSurvivesRestart, TestStreamPollRetainsBatchOnSinkFailure
//   - TestBatchIntentWriteFailureDoesNotAdvanceCursor ->
//     TestStreamPollRetainsBatchOnSinkFailure (sink failure retains the
//     durable batch, cursor never advances)
//   - TestPauseDuringBatchPreventsEffect -> TestStreamPauseFencesPendingBatch
//   - TestBatchAgentRemainderKeepsExactPayloadAndRevision ->
//     TestSettleStreamEventsAgentSink + TestStreamEditFencesPendingBatch;
//     the stale-occurrence delivery fence is now Host.AuthorizeOccurrence
//     (TestAuthorizeOccurrenceReauthorizesAtSendTime in host_test.go)
//   - TestBatchDroppedEventsDoNotDependOnSeenWindow ->
//     TestEventGapSurfacedOnSeenEviction
//   - TestBatchOccurrenceRestartDoesNotFetchAgain ->
//     TestStreamBatchSurvivesRestart
//   - TestBatchRepairsExactPendingAdmissionOnly ->
//     TestNamedTaskRoutineSurvivesAdmissionAndRejectsForeignConversation
//     (routine_task_test.go): the admission fingerprint is the durable fence
//   - TestBatchAssignmentWakeReservationSurvivesEnqueueFailure ->
//     internal/routines/assignments_test.go (ReserveAssignmentWake
//     idempotency, WakesToday stays 1 across replay)
//   - TestCorruptBatchCannotAdvanceCursorOrFetchAnotherSource ->
//     TestEventProbeRevisionDoesNotDiscardPendingBatch +
//     TestStreamEditFencesPendingBatch (durable batch fences, never silently
//     advances)
//   - TestWatcherWakeEnqueueFailureReplaysSavedIntent ->
//     internal/routines/watchers_test.go (.probe-result receipt replay
//     without refetch)
