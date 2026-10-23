import SwiftUI

#if os(iOS)
import JKMeetingShared_iOS
#else
import JKMeetingShared_macOS
#endif

struct RecordingView: View {
    @StateObject private var viewModel = RecordingViewModel(manager: AppleRecordingManager())

    var body: some View {
        VStack(spacing: 20) {
            Text("JKMeeting")
                .font(.largeTitle.bold())

            Text(statusText)
                .font(.headline)
                .multilineTextAlignment(.center)

            if case .recorded(let metadata, _) = viewModel.state {
                metadataView(metadata)
            }
            if case .playing(let metadata, _) = viewModel.state {
                metadataView(metadata)
            }
            if case .failed(_, let message) = viewModel.state {
                Text(message)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
            }

            controls
            transcriptionControls
        }
        .padding(24)
        .frame(maxWidth: 480)
        .onDisappear { viewModel.cancelTranscription() }
    }

    @ViewBuilder
    private var controls: some View {
        switch viewModel.state {
        case .recording:
            Button("停止录音", action: viewModel.stopRecording)
                .buttonStyle(.borderedProminent)
        case .recorded, .playing:
            HStack {
                Button("重新录音", action: viewModel.startRecording)
                Button(viewModelIsPlaying ? "停止播放" : "播放录音") {
                    if viewModelIsPlaying {
                        viewModel.stopPlayback()
                    } else {
                        viewModel.playRecording()
                    }
                }
                .buttonStyle(.borderedProminent)
            }
        case .requestingPermission, .startingCapture, .stopping:
            ProgressView()
        default:
            Button("开始录音", action: viewModel.startRecording)
                .buttonStyle(.borderedProminent)
        }
    }

    @ViewBuilder
    private var transcriptionControls: some View {
        switch viewModel.state {
        case .recorded, .playing:
            VStack(spacing: 10) {
                switch viewModel.transcriptionPhase {
                case .unavailable:
                    Text("转写未配置：请从开发环境注入 API 地址、S3 地址和访问令牌")
                        .foregroundStyle(.secondary)
                case .ready:
                    Button("上传并转写", action: viewModel.startTranscription)
                        .buttonStyle(.borderedProminent)
                case .uploading:
                    ProgressView("正在上传录音")
                    Button("取消上传", action: viewModel.cancelTranscription)
                case .transcribing:
                    ProgressView("正在转写，请稍候")
                    Button("取消等待", action: viewModel.cancelTranscription)
                case .done:
                    Text("转写结果").font(.headline)
                    Text(viewModel.transcriptionText ?? "（无语音内容）")
                        .textSelection(.enabled)
                    remotePlaybackSection
                case .failed(let message):
                    Text(message).foregroundStyle(.red)
                    Button("重新上传并转写", action: viewModel.startTranscription)
                }
            }
            .multilineTextAlignment(.center)
        default:
            EmptyView()
        }
    }

    @ViewBuilder
    private var remotePlaybackSection: some View {
        if let remote = viewModel.remotePlayback {
            if remote.isHLSReady {
                RemoteHLSControls(playback: viewModel.hlsPlayback, viewModel: viewModel)
            } else if remote.transcodeStatus == "failed" {
                Text("服务端转码失败，仅可本地回放").foregroundStyle(.secondary)
            } else if remote.transcodeStatus != "none" {
                ProgressView("服务端正在生成多码率回放")
            }
        }
    }

    private var viewModelIsPlaying: Bool {
        if case .playing = viewModel.state { return true }
        return false
    }

    private var statusText: String {
        switch viewModel.state {
        case .idle:
            return "准备就绪"
        case .requestingPermission:
            return "正在请求麦克风权限"
        case .startingCapture:
            return "正在启动录音"
        case .recording:
            return "录音中"
        case .stopping:
            return "正在保存录音"
        case .recorded(_, let reason):
            switch reason {
            case .user: return "录音已停止，可回放"
            case .durationLimit: return "录音已达 5 分钟上限，可回放并转写"
            case .interrupted: return "录音被系统中断，已保存可用部分"
            }
        case .playing:
            return "正在播放"
        case .failed:
            return "录音失败"
        }
    }

    @ViewBuilder
    private func metadataView(_ metadata: RecordingMetadata) -> some View {
        VStack(spacing: 4) {
            Text("\(metadata.durationMs) ms · \(metadata.sampleRate) Hz · \(metadata.channels) ch")
            Text("\(metadata.sizeBytes) bytes")
                .foregroundStyle(.secondary)
        }
        .font(.footnote)
    }
}

/// 远程 HLS 回放与码率档位选择；单独观察 `HLSPlaybackController`，否则嵌套对象的变化不会刷新界面。
private struct RemoteHLSControls: View {
    @ObservedObject var playback: HLSPlaybackController
    let viewModel: RecordingViewModel

    var body: some View {
        VStack(spacing: 8) {
            Button(playback.isPlaying ? "停止远程回放" : "远程 HLS 回放") {
                if playback.isPlaying {
                    viewModel.stopRemote()
                } else {
                    Task { await viewModel.playRemote() }
                }
            }
            .buttonStyle(.bordered)
            if playback.isPlaying, !playback.variants.isEmpty {
                Picker("码率", selection: Binding(get: { playback.selectedVariant }, set: { playback.select($0) })) {
                    Text("自动").tag(HLSVariant?.none)
                    ForEach(playback.variants, id: \.peakBitRate) { variant in
                        Text("\(variant.kbps) kbps").tag(HLSVariant?.some(variant))
                    }
                }
                .pickerStyle(.segmented)
                Text(statusLine).font(.footnote).foregroundStyle(.secondary)
            }
        }
    }

    private var statusLine: String {
        let current = playback.indicatedBitrate.map { "当前档位 \(Int($0 / 1000)) kbps" } ?? "等待档位信息"
        guard let latency = playback.lastSwitchLatencyMs else { return current }
        return "\(current) · 切换耗时 \(Int(latency)) ms"
    }
}
