package com.phantom.vpn

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

enum class ConnectionStatus { IDLE, CONNECTING, CONNECTED, ERROR }

data class VpnState(
    val status: ConnectionStatus = ConnectionStatus.IDLE,
    val message: String = "",
    /** Which saved config this status refers to - null whenever status is IDLE. */
    val activeConfigId: String? = null,
)

/** Simple in-process state bridge between [PhantomVpnService] and the UI. */
object VpnStateHolder {
    private val _state = MutableStateFlow(VpnState())
    val state: StateFlow<VpnState> = _state

    fun update(status: ConnectionStatus, message: String = "", configId: String? = null) {
        _state.value = VpnState(status, message, if (status == ConnectionStatus.IDLE) null else configId)
    }
}
