#!/usr/bin/env python3
"""生成 docs/architecture.png。

只用于维护文档插图，不参与构建。依赖 Pillow 与 macOS 自带的
Hiragino Sans GB 字体（W3 用于正文，W6 用于标题）：

    python3 docs/architecture.py
"""

import os
import math
from PIL import Image, ImageDraw, ImageFont

S = 2
W, H = 1680, 1110
IMG = Image.new("RGB", (W * S, H * S), "#F7F9FC")
D = ImageDraw.Draw(IMG)

FONT_PATH = "/System/Library/Fonts/Hiragino Sans GB.ttc"

INK = "#1F2937"
MUTED = "#5F6B7A"
FAINT = "#8C97A6"
BLUE = "#3B6FB6"
BLUE_DARK = "#2C548C"
ORANGE = "#B9702F"



def font(size, bold=False):
    return ImageFont.truetype(FONT_PATH, int(size * S), index=2 if bold else 0)


def rect(x, y, w, h, fill, outline, radius=14, width=1.4):
    D.rounded_rectangle(
        [x * S, y * S, (x + w) * S, (y + h) * S],
        radius=radius * S,
        fill=fill,
        outline=outline,
        width=max(1, int(width * S)),
    )


def txt(x, y, s, size=16, bold=False, fill=INK, anchor="la"):
    D.text((x * S, y * S), s, font=font(size, bold), fill=fill, anchor=anchor)


def text_w(s, size, bold=False):
    return D.textlength(s, font=font(size, bold)) / S


def line(p1, p2, fill, width=2.0, dash=None, gap=6):
    x1, y1 = p1
    x2, y2 = p2
    if dash is None:
        D.line([(x1 * S, y1 * S), (x2 * S, y2 * S)], fill=fill, width=max(1, int(width * S)))
        return
    length = math.hypot(x2 - x1, y2 - y1)
    if length == 0:
        return
    ux, uy = (x2 - x1) / length, (y2 - y1) / length
    t = 0.0
    while t < length:
        e = min(t + dash, length)
        D.line(
            [((x1 + ux * t) * S, (y1 + uy * t) * S), ((x1 + ux * e) * S, (y1 + uy * e) * S)],
            fill=fill,
            width=max(1, int(width * S)),
        )
        t = e + gap


def head(x, y, angle, fill, size=8.5):
    spread = math.pi / 6.5
    pts = [
        (x, y),
        (x - size * math.cos(angle - spread), y - size * math.sin(angle - spread)),
        (x - size * math.cos(angle + spread), y - size * math.sin(angle + spread)),
    ]
    D.polygon([(px * S, py * S) for px, py in pts], fill=fill)


def arrow(p1, p2, fill, width=2.0, size=8.5, dash=None, gap=6):
    angle = math.atan2(p2[1] - p1[1], p2[0] - p1[0])
    # 让线段在箭头前收一点，避免箭头压住线头
    ex = p2[0] - math.cos(angle) * size * 0.7
    ey = p2[1] - math.sin(angle) * size * 0.7
    line(p1, (ex, ey), fill, width=width, dash=dash, gap=gap)
    head(p2[0], p2[1], angle, fill, size=size)


# ---------- 标题 ----------
txt(60, 40, "devin-2api lite", 40, bold=True)
txt(60, 96, "OpenAI 协议入口 · 手写 protobuf + Connect 帧 · 单二进制、无 protoc", 17, fill=MUTED)

badge_w = text_w("无 protoc · 无生成代码", 13.5, bold=True) + 34
rect(1620 - badge_w, 46, badge_w, 38, "#E8F0FE", "#C3D6F5", radius=19, width=1)
txt(1620 - badge_w / 2, 65, "无 protoc · 无生成代码", 13.5, bold=True, fill=BLUE_DARK, anchor="mm")

# ---------- 客户端 ----------
rect(50, 200, 250, 390, "#F3F4F7", "#DFE3EA", radius=16)
txt(70, 218, "客户端", 21, bold=True)
txt(70, 248, "OpenAI 兼容 HTTP", 12.5, fill=FAINT)

client_boxes = [
    ("OpenAI SDK", "chat / responses"),
    ("Codex CLI", "responses"),
    ("IDE 插件", "Continue / Cline"),
]
cy = 268
for title, sub in client_boxes:
    rect(70, cy, 210, 56, "#FFFFFF", "#D5DAE3", radius=10, width=1)
    txt(84, cy + 9, title, 14.5, bold=True)
    txt(84, cy + 31, sub, 11.5, fill=FAINT)
    cy += 70

txt(70, 492, "POST /v1/chat/completions", 11.5, fill=MUTED)
txt(70, 513, "POST /v1/responses", 11.5, fill=MUTED)
txt(70, 534, "GET  /v1/models", 11.5, fill=MUTED)

# 客户端 → 第 1 层
arrow((300, 275), (446, 275), BLUE, width=2.2)
txt(304, 248, "HTTP + SSE", 12, fill=BLUE_DARK)


# ---------- 请求生命周期 ----------
rect(50, 640, 250, 290, "#FFFFFF", "#DFE3EA", radius=16)
txt(70, 658, "请求生命周期", 17, bold=True)
txt(70, 686, "一次 /v1 请求的 5 步", 11.5, fill=FAINT)
steps = [
    "① 解码 JSON → 中间请求",
    "② 组装上游 GetChatMessage",
    "③ 发送 Connect 帧",
    "④ 响应帧 → 12 类事件",
    "⑤ 事件 → SSE / JSON",
]
sy = 720
for step in steps:
    txt(70, sy, step, 12.5, fill=MUTED)
    sy += 39

# ---------- 网关面板 ----------
rect(420, 160, 700, 770, "#F4F8FF", "#C9DBF5", radius=18, width=1.6)
txt(446, 176, "网关进程", 17.5, bold=True, fill=BLUE_DARK)
txt(1094, 181, "localhost:8080", 12.5, fill=FAINT, anchor="ra")

layers = [
    ("HTTP 接入层", "internal/app", "ServeMux 路由 · 并发上限 · API Key · 错误映射"),
    ("协议编解码", "internal/api/openai", "请求 JSON → llm；llm 事件 → SSE / JSON"),
    ("语义中间层", "internal/llm", "RequestMessages · AssistantMessage · 12 类 ResponseEvent"),
    ("Devin 适配器", "internal/adapter/devin", "请求组装 · 工具说明改写 · 响应帧 → 事件状态机"),
    ("Connect 客户端", "internal/connect", "5 字节帧 · gzip · 一元调用 · 错误信封"),
]

layer_x, layer_w, layer_h = 450, 640, 100
top = 225
gap = 30
for index, (title, pkg, sub) in enumerate(layers):
    y = top + index * (layer_h + gap)
    rect(layer_x, y, layer_w, layer_h, "#FFFFFF", "#C6D8F0", radius=12, width=1.5)
    rect(layer_x + 14, y + 18, 5, layer_h - 36, "#6F9FD8", "#6F9FD8", radius=2.5, width=0)
    txt(layer_x + 32, y + 17, title, 21, bold=True)
    txt(layer_x + layer_w - 20, y + 23, pkg, 12.5, fill=FAINT, anchor="ra")
    txt(layer_x + 32, y + 55, sub, 15.5, fill=MUTED)
    if index < len(layers) - 1:
        arrow((layer_x + layer_w / 2, y + layer_h + 3), (layer_x + layer_w / 2, y + layer_h + gap - 3), "#8FA9C9", width=1.8, size=7)

txt(452, 872, "流式路径：上游帧 → 中间事件 → typed SSE；非流式：聚合为一条 AssistantMessage", 13.5, fill=FAINT)


# ---------- 图例 ----------
line((452, 905), (492, 905), BLUE, width=2.2)
txt(500, 898, "请求方向", 12, fill=FAINT)
line((576, 905), (616, 905), "#9AA7B8", width=1.8, dash=7, gap=5)
txt(624, 898, "响应 / 事件回传", 12, fill=FAINT)

# ---------- 上游 ----------
rect(1250, 580, 390, 350, "#FFF8F0", "#F0CFAE", radius=16, width=1.6)
txt(1272, 598, "Devin Connect", 20, bold=True, fill="#8A4E1C")
txt(1272, 628, "server.codeium.com", 12.5, fill="#A9886A")

rect(1272, 650, 346, 76, "#FFFFFF", "#EBC9A6", radius=10, width=1)
txt(1288, 661, "GetCascadeModelConfigs", 14, bold=True)
txt(1288, 684, "一元 · 模型目录", 11.5, fill=FAINT)

rect(1272, 742, 346, 92, "#FFFFFF", "#EBC9A6", radius=10, width=1)
txt(1288, 752, "GetChatMessage", 15, bold=True)
txt(1288, 776, "服务端流 · 对话 / 工具调用", 11.5, fill=FAINT)

txt(1272, 850, "Auth · Basic <token>-<token>", 11.5, fill="#8A6B50")
txt(1272, 872, "身份 · chisel / 3000.2.17", 11.5, fill="#8A6B50")
txt(1272, 894, "每轮新生成 Trajectory / Cascade / Execution ID", 10.5, fill="#B08F72")

# 第 5 层 → 上游
arrow((1092, 792), (1246, 792), BLUE, width=2.2)
txt(1130, 758, "protobuf", 11.5, fill=BLUE_DARK)
txt(1130, 773, "Connect 帧", 11.5, fill=BLUE_DARK)
# 上游 → 第 5 层（响应）
arrow((1246, 836), (1092, 836), "#9AA7B8", width=1.8, size=7.5, dash=7, gap=5)
txt(1130, 844, "响应帧 /", 11, fill=FAINT)
txt(1130, 858, "结束信封", 11, fill=FAINT)

# ---------- 底部 ----------
rect(50, 975, 1590, 95, "#EEF2F7", "#DCE3ED", radius=14, width=1.2)
txt(70, 992, "辅助模块：config.yaml 配置 · debuglog 每请求日志 · httpproxy 代理与连接池 · docs/upstream-protocol.md 字段表", 14.5, fill=MUTED)
txt(70, 1024, "质量：85 个测试（含 httptest 假上游端到端）· go vet · gofmt · go test -race · 单二进制约 7 MB", 12.5, fill=FAINT)
txt(1620, 1004, "MIT", 12.5, fill=FAINT, anchor="ra")

IMG = IMG.resize((W, H), Image.LANCZOS)
out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "architecture.png")
IMG.save(out)
print("saved", out, IMG.size)
