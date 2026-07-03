package com.phantom.vpn

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

enum class ConnectionStatus { IDLE, CONNECTING, CONNECTED, ERROR }

data class VpnState(
    val status: ConnectionStatus = ConnectionStatus.IDLE,
    val message: String = "",
)

/** Simple in-process state bridge between [PhantomVpnService] and the UI. */
object VpnStateHolder {
    private val _state = MutableStateFlow(VpnState())
    val state: StateFlow<VpnState> = _state

    fun update(status: ConnectionStatus, message: String = "") {
        _state.value = VpnState(status, message)
    }
}
