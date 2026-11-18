package net.neciparmagan.trdriver.calls

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CallNetworkPolicyTest {
    private fun net(
        id: String,
        validated: Boolean = true,
        hasInternet: Boolean = true,
        isDefault: Boolean = false,
        wifi: Boolean = false,
        cellular: Boolean = false,
        ethernet: Boolean = false,
    ) = CallNetworkPolicy.NetChoice(
        id = id,
        validated = validated,
        hasInternet = hasInternet,
        isDefault = isDefault,
        wifi = wifi,
        cellular = cellular,
        ethernet = ethernet,
        vpn = false,
    )

    @Test
    fun backupStaysUnmeteredAndCallsDoNotShareThatConstraint() {
        assertFalse(CallNetworkPolicy.backupAllowsMetered())
        assertTrue(CallNetworkPolicy.callsAllowMetered())
        assertFalse(CallNetworkPolicy.shouldBindProcessToNetwork())
        assertTrue(CallNetworkPolicy.HANDOVER_RECONNECT_MS in 100L..2_000L)
    }

    @Test
    fun validatedMobileBeatsUnvalidatedWifi() {
        val wifi = net("wifi", validated = false, isDefault = true, wifi = true)
        val cell = net("cell", validated = true, cellular = true)
        assertEquals("cell", CallNetworkPolicy.choose(listOf(wifi, cell))?.id)
    }

    @Test
    fun defaultWifiWinsWhenBothAreValidated() {
        val wifi = net("wifi", isDefault = true, wifi = true)
        val cell = net("cell", cellular = true)
        assertEquals("wifi", CallNetworkPolicy.choose(listOf(cell, wifi))?.id)
    }

    @Test
    fun ethernetIsUsableAndOfflineNetworksAreIgnored() {
        val offline = net("wifi", hasInternet = false, wifi = true, isDefault = true)
        val eth = net("eth", ethernet = true)
        assertEquals("eth", CallNetworkPolicy.choose(listOf(offline, eth))?.id)
        assertNull(CallNetworkPolicy.choose(listOf(offline)))
    }

    @Test
    fun handoverOnlyWhenTheChosenNetworkChanges() {
        val wifi = net("wifi", wifi = true, isDefault = true)
        val cell = net("cell", cellular = true, isDefault = true)
        assertFalse(CallNetworkPolicy.handover(null, wifi))
        assertFalse(CallNetworkPolicy.handover("wifi", wifi))
        assertTrue(CallNetworkPolicy.handover("wifi", cell))
        assertFalse(CallNetworkPolicy.handover("wifi", null))
    }
}