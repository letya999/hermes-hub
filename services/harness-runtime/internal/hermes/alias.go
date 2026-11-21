package hermes

// Contract seam (CHG-0337): the wire types the supervisor, communication and
// relays share still live in internal/runtime until contracts/runtime/v1
// lands. The workload aliases them so moved code compiles unchanged and both
// sides always decode the same shape — never a copy.
import (
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type ExecuteRequest = hubruntime.ExecuteRequest
type ExecuteResponse = hubruntime.ExecuteResponse
type RunReference = hubruntime.RunReference
type RunControl = hubruntime.RunControl
type GoalRequest = hubruntime.GoalRequest
type GoalResult = hubruntime.GoalResult
type UsageRequest = hubruntime.UsageRequest
type SessionUsage = hubruntime.SessionUsage
type CompactRequest = hubruntime.CompactRequest
type ArtifactRef = hubruntime.ArtifactRef
type ArtifactRequest = hubruntime.ArtifactRequest
type RuntimeHealth = hubruntime.RuntimeHealth
type RuntimeStatus = hubruntime.RuntimeStatus
type SessionReadRequest = hubruntime.SessionReadRequest
type ScriptRequest = hubruntime.ScriptRequest
type SelfEnvRequest = hubruntime.SelfEnvRequest
type SkillApplyRequest = hubruntime.SkillApplyRequest
type SkillCredsRequest = hubruntime.SkillCredsRequest
type RunAuthorization = hubruntime.RunAuthorization
type KubernetesIsolationReceipt = hubruntime.KubernetesIsolationReceipt

const MaxPromptBytes = hubruntime.MaxPromptBytes
const RunStreamContentType = hubruntime.RunStreamContentType
const RunAuthorizationPath = hubruntime.RunAuthorizationPath
const RunDeliveryHeader = hubruntime.RunDeliveryHeader
const KubernetesIsolationPath = hubruntime.KubernetesIsolationPath

var ReadRunStream = hubruntime.ReadRunStream
var ValidSessionReadRequest = hubruntime.ValidSessionReadRequest
var RunBudget = hubruntime.RunBudget
var StreamIdleDeadline = hubruntime.StreamIdleDeadline
var IdleReader = hubruntime.IdleReader

var toolHubRelayHandler = hubruntime.ToolHubRelayHandler

var modelRelayRegistryHandler = hubruntime.ModelRelayRegistryHandler

var signedEgressRelayHandler = hubruntime.SignedEgressRelayHandler
var relayRunAuthorityFromEnv = hubruntime.RelayRunAuthorityFromEnv
