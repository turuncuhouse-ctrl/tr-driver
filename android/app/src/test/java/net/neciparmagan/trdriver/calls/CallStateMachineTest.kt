package net.neciparmagan.trdriver.calls

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class CallStateMachineTest {

    @Test
    fun incomingCallAcceptTalkAndHangupTransitionsCleanly() {
        var state = CallStateMachine.idle()
        assertFalse(state.isLive)

        val ring = CallStateMachine.reduce(
            state,
            CallEvent.ReceiveIncoming(
                callId = "c1",
                remoteAddress = "+905551112233",
                remoteDisplayName = "Ahmet Demir",
                matchedFromContacts = true,
                contactLabel = "Cep",
                nowMs = 1000L,
            ),
        )
        assertTrue(ring.accepted)
        state = ring.state
        assertEquals(CallPhase.INCOMING_RINGING, state.phase)
        assertTrue(state.shouldRing)
        assertTrue(state.shouldShowLockScreenUi)
        assertTrue(state.canAnswer)
        assertTrue(state.canReject)

        val accept = CallStateMachine.reduce(state, CallEvent.UserAccept("c1", nowMs = 2000L))
        assertTrue(accept.accepted)
        state = accept.state
        assertEquals(CallPhase.CONNECTING, state.phase)
        assertFalse(state.shouldRing)

        val active = CallStateMachine.reduce(state, CallEvent.MediaConnected("c1", nowMs = 2500L))
        assertTrue(active.accepted)
        state = active.state
        assertEquals(CallPhase.ACTIVE, state.phase)
        assertTrue(state.shouldAcquireProximityLock)
        assertEquals(5000L, state.elapsedMs(7500L))

        state = CallStateMachine.reduce(state, CallEvent.SetMuted("c1", true)).state
        assertTrue(state.muted)

        state = CallStateMachine.reduce(state, CallEvent.SetSpeaker("c1", true)).state
        assertTrue(state.speakerOn)
        assertFalse(state.shouldAcquireProximityLock)

        val ended = CallStateMachine.reduce(state, CallEvent.UserHangup("c1", nowMs = 9500L))
        assertTrue(ended.accepted)
        assertTrue(ended.cleanupRequired)
        state = ended.state
        assertEquals(CallPhase.ENDED, state.phase)
        assertEquals(CallEndReason.LOCAL_HANGUP, state.endReason)
        assertFalse(state.muted)
        assertFalse(state.speakerOn)
        assertFalse(state.shouldHoldAudioFocus)
        assertFalse(state.shouldAcquireProximityLock)
        assertTrue(state.isTerminal)
    }

    @Test
    fun outgoingCancelBeforeAnswerReportsCancelled() {
        var state = CallStateMachine.reduce(
            CallStateMachine.idle(),
            CallEvent.StartOutgoing("c2", "+905551112233", "Ahmet", true, nowMs = 100L),
        ).state
        assertEquals(CallPhase.OUTGOING_DIALING, state.phase)

        state = CallStateMachine.reduce(state, CallEvent.RemoteRinging("c2")).state
        assertEquals(CallPhase.OUTGOING_RINGING, state.phase)
        assertTrue(state.shouldPlayRingback)

        val cancelled = CallStateMachine.reduce(state, CallEvent.UserHangup("c2", nowMs = 3000L))
        assertTrue(cancelled.accepted)
        assertEquals(CallEndReason.CANCELLED, cancelled.state.endReason)
    }

    @Test
    fun rejectsConcurrentIncomingCallWhileLive() {
        val first = CallStateMachine.reduce(
            CallStateMachine.idle(),
            CallEvent.ReceiveIncoming("c1", "+905551112233", "A", true, nowMs = 10L),
        ).state

        val second = CallStateMachine.reduce(
            first,
            CallEvent.ReceiveIncoming("c2", "+905559998877", "B", false, nowMs = 20L),
        )
        assertFalse(second.accepted)
        assertEquals("busy", second.rejectionReason)
        assertEquals("c1", second.state.callId)
    }

    @Test
    fun toStringNeverLeaksPiiOrSecrets() {
        val state = CallSessionState(
            callId = "call-99",
            direction = CallDirection.INCOMING,
            phase = CallPhase.INCOMING_RINGING,
            remoteAddress = "+905551112233",
            remoteDisplayName = "Secret Person",
            matchedFromContacts = true,
        )
        val stateStr = state.toString()
        assertFalse(stateStr.contains("5551112233"))
        assertFalse(stateStr.contains("Secret Person"))

        val cfg = LocalTelConfig(
            enabled = true,
            username = "1001",
            secret = "my-sip-password",
            serverUrl = "sip.secret.local",
        )
        val cfgStr = cfg.toString()
        assertFalse(cfgStr.contains("my-sip-password"))
        assertFalse(cfgStr.contains("1001"))

        val reqDto = LocalTelConfigRequestDto(
            enabled = true,
            providerType = "sip",
            serverUrl = "sip.local",
            secret = "my-sip-password",
        )
        assertFalse(reqDto.toString().contains("my-sip-password"))

        val sigDto = LocalTelSignalDto(
            type = "invite",
            callId = "c1",
            number = "+905551112233",
            displayName = "Secret Name",
        )
        assertFalse(sigDto.toString().contains("5551112233"))
        assertFalse(sigDto.toString().contains("Secret Name"))
    }

    @Test
    fun formatsDurationProperly() {
        assertEquals("00:00", CallStateMachine.formatDuration(0L))
        assertEquals("00:09", CallStateMachine.formatDuration(9_400L))
        assertEquals("02:05", CallStateMachine.formatDuration(125_000L))
        assertEquals("1:01:05", CallStateMachine.formatDuration(3_665_000L))
    }
}
