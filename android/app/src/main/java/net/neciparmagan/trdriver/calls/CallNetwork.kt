package net.neciparmagan.trdriver.calls

import android.net.Network
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.Socket
import javax.net.SocketFactory

/**
 * Backup and other bulk sync stay on unmetered Wi-Fi. LocalTel calls do not:
 * signaling and media may use Wi-Fi or mobile data, and a handover picks a
 * new network instead of pinning the process to the old one.
 */
object CallNetworkPolicy {
    /** How long to wait for the new network to settle before reopening the socket. */
    const val HANDOVER_RECONNECT_MS = 300L

    fun backupAllowsMetered(): Boolean = false

    fun callsAllowMetered(): Boolean = true

    /** Binding the process would force backup and calls onto the same network. */
    fun shouldBindProcessToNetwork(): Boolean = false

    data class NetChoice(
        val id: String,
        val validated: Boolean,
        val hasInternet: Boolean,
        val isDefault: Boolean,
        val wifi: Boolean,
        val cellular: Boolean,
        val ethernet: Boolean,
        val vpn: Boolean,
    )

    /**
     * Prefer a validated default network. When Wi-Fi is up but not validated,
     * a validated mobile network wins so a call is not stuck on a dead LAN.
     */
    fun choose(networks: List<NetChoice>): NetChoice? {
        val usable = networks.filter {
            it.hasInternet && (it.wifi || it.cellular || it.ethernet || it.vpn)
        }
        val validated = usable.filter { it.validated }
        val pool = if (validated.isNotEmpty()) validated else usable
        return pool.maxByOrNull { score(it) }
    }

    /** True only when an already-chosen network is replaced. The first pick is not a handover. */
    fun handover(previousId: String?, next: NetChoice?): Boolean =
        previousId != null && next != null && next.id != previousId

    private fun score(n: NetChoice): Int {
        var value = 0
        if (n.validated) value += 100
        if (n.isDefault) value += 50
        if (n.wifi || n.ethernet) value += 10
        if (n.cellular) value += 10
        if (n.vpn) value += 5
        return value
    }
}

/**
 * Network LocalTel sockets should use. The listener updates it; backup never
 * writes it and never binds the process.
 */
object CallNetwork {
    @Volatile
    private var active: Network? = null

    fun update(network: Network?) {
        active = network
    }

    fun bind(socket: Socket) {
        val network = active ?: return
        runCatching { network.bindSocket(socket) }
    }

    fun bind(socket: DatagramSocket) {
        val network = active ?: return
        runCatching { network.bindSocket(socket) }
    }

    fun socketFactory(): SocketFactory = object : SocketFactory() {
        override fun createSocket(): Socket = open()
        override fun createSocket(host: String, port: Int): Socket = open(host, port)
        override fun createSocket(host: String, port: Int, local: InetAddress, localPort: Int): Socket =
            open(host, port, local, localPort)

        override fun createSocket(address: InetAddress, port: Int): Socket = open(address, port)
        override fun createSocket(
            address: InetAddress,
            port: Int,
            local: InetAddress,
            localPort: Int,
        ): Socket = open(address, port, local, localPort)

        private fun factory(): SocketFactory? = active?.socketFactory

        private fun open(): Socket =
            factory()?.let { runCatching { it.createSocket() }.getOrNull() } ?: Socket()

        private fun open(host: String, port: Int): Socket =
            factory()?.let { runCatching { it.createSocket(host, port) }.getOrNull() } ?: Socket(host, port)

        private fun open(host: String, port: Int, local: InetAddress, localPort: Int): Socket =
            factory()?.let { runCatching { it.createSocket(host, port, local, localPort) }.getOrNull() }
                ?: Socket(host, port, local, localPort)

        private fun open(address: InetAddress, port: Int): Socket =
            factory()?.let { runCatching { it.createSocket(address, port) }.getOrNull() }
                ?: Socket(address, port)

        private fun open(address: InetAddress, port: Int, local: InetAddress, localPort: Int): Socket =
            factory()?.let { runCatching { it.createSocket(address, port, local, localPort) }.getOrNull() }
                ?: Socket(address, port, local, localPort)
    }
}
