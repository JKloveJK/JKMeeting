package com.jkmeeting.android

import android.Manifest
import android.content.pm.PackageManager
import android.media.MediaPlayer
import android.os.Bundle
import android.os.SystemClock
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import java.io.File

data class RecordingUiState(
    val status: String = "就绪：录音将经 C++ core（48k→16k 重采样）处理后保存为 WAV",
    val isRecording: Boolean = false,
    val result: RecordingResult? = null,
)

class RecordingViewModel(private val recorder: AudioRecorder) : ViewModel() {
    private val _state = MutableStateFlow(RecordingUiState())
    val state: StateFlow<RecordingUiState> = _state

    fun toggleRecording() {
        if (_state.value.isRecording) {
            recorder.stop()
            return
        }
        _state.value = _state.value.copy(isRecording = true, status = "录音中…（最长 30 秒）")
        viewModelScope.launch {
            try {
                val result = recorder.record()
                _state.value = RecordingUiState(
                    status = "完成：${result.durationMs} ms，core 处理 ${result.coreElapsedMs} ms，" +
                        "语音 ${result.stats.getOrElse(4) { 0 }} ms / 静音 ${result.stats.getOrElse(5) { 0 }} ms",
                    result = result,
                )
            } catch (error: Exception) {
                _state.value = RecordingUiState(status = "失败：${error.message}")
            }
        }
    }

    fun play() {
        val file = _state.value.result?.file ?: return
        MediaPlayer().apply {
            setDataSource(file.absolutePath)
            prepare()
            start()
            setOnCompletionListener { release() }
        }
    }
}

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        val startedAt = SystemClock.uptimeMillis()
        super.onCreate(savedInstanceState)
        val viewModel = ViewModelProvider(
            this,
            object : ViewModelProvider.Factory {
                @Suppress("UNCHECKED_CAST")
                override fun <T : ViewModel> create(modelClass: Class<T>): T =
                    RecordingViewModel(AudioRecorder(File(filesDir, "recordings"))) as T
            },
        )[RecordingViewModel::class.java]

        setContent {
            MaterialTheme {
                RecordingScreen(viewModel)
            }
        }
        // 冷启动口径：进程创建 -> 首帧绘制完成（与路线 A4 的 am start -W 互为印证）。
        window.decorView.post {
            Log.i(TAG, "cold_start_ms=${SystemClock.uptimeMillis() - startedAt}")
        }
    }

    companion object {
        const val TAG = "JKMeetingAndroid"
    }
}

@Composable
private fun RecordingScreen(viewModel: RecordingViewModel) {
    val state by viewModel.state.collectAsStateWithLifecycle()
    val permissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (granted) viewModel.toggleRecording()
    }
    val context = androidx.compose.ui.platform.LocalContext.current

    Column(
        modifier = Modifier.fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("JKMeeting Android", style = MaterialTheme.typography.headlineMedium)
        Text(state.status)
        Button(onClick = {
            val granted = ContextCompat.checkSelfPermission(
                context, Manifest.permission.RECORD_AUDIO,
            ) == PackageManager.PERMISSION_GRANTED
            if (granted) viewModel.toggleRecording() else permissionLauncher.launch(Manifest.permission.RECORD_AUDIO)
        }) {
            Text(if (state.isRecording) "停止录音" else "开始录音")
        }
        Button(onClick = { viewModel.play() }, enabled = state.result != null) {
            Text("回放")
        }
        state.result?.let { result ->
            Text("文件：${result.file.name}（${result.outputSamples} 样本 @16kHz）")
        }
    }
}
