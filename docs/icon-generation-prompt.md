# Lcoder 项目图标 · AIGC 生成提示词

> 用途：为 Lcoder 生成项目图标 / Logo / 吉祥物，采用**二次元主题风格**的 AI 编程助手形象（不使用语言吉祥物，保持通用）。

---

## 一、项目特色总结（品牌气质定位）

Lcoder 是一个**纯 Go 实现的 AI 编程助手**，单二进制、零依赖、终端原生。提炼出的品牌关键词：

| 维度 | 特色 | 可视觉化的元素 |
|------|------|----------------|
| **语言** | 纯 Go，单二进制 `go build` 即用 | `{}` 花括号、`>_` 提示符、`0101` 代码流 |
| **形态** | 终端原生 TUI（Bubble Tea） | 终端窗口、命令行光标、荧光边框 |
| **核心能力** | 多模式 Agent（code/plan/explore/review） | 四宫格 / 四种表情徽章 |
| **会话管理** | 任意消息分叉、克隆新会话 | 树状分支、光缆分叉 |
| **安全** | 路径安全守卫、四级权限引擎 | 能量护盾、锁链、发光警戒线 |
| **上下文** | 分层压缩 + prompt cache | 层级堆叠的透明卡片 |
| **协作** | 子 Agent 并行 + swarm 集群 | 迷你分身、蜂群光点 |
| **可观测** | 审计日志、统计报表 | 终端滚动日志流 |

**品牌气质**：冷静、高效、极客、治愈（像一位可靠的 AI 编程搭档）。
**主色**（来自 TUI Nord 主题）：青色 `#88C0D0`、蓝 `#5E81AC`，深色背景 `#2E3440`。
**设计原则**：图标须在 32px 小尺寸下依然可辨识（主形象 + 1~2 个高对比元素即可）。

**主角形象定位**：不使用 Gopher 等语言吉祥物，改用通用的二次元 AI 助手形象 —— 推荐 **Q 版（chibi）银发魔法使少女**（手持/悬浮全息终端，赛博魔法师设定），备选 **Q 版全息使魔 / 迷你机器人**。人形 Q 版形象在二次元风格中辨识度高、易于表达表情与情绪。

---

## 二、推荐提示词（英文版，AIGC 工具效果最佳）

### 变体 A：赛博魔法使少女（首推 · 细化版）

**图标概念**：一位 Q 版银发魔法使少女，她是 AI 编程助手的人格化形象。她悬浮在一面发光全息终端窗口前，双手向外摊开作"施法引导"状，代码像魔法能量一样从终端流入她手中，再从指尖化作光点散开 —— 表达"AI 与程序员之间的双向协作"。终端右上角延伸出一条分叉的银色光缆，象征会话分支；她身后悬浮着护盾与迷你分身，分别对应路径安全守卫与子 Agent 集群。整体呈圆形徽章构图，四角元素向中心收拢，小尺寸下依然清晰可辨。

> **Prompt A（英文 · 结构化分块版，逐段组成一张图）：**
>
> 1. STYLE: clean flat anime illustration, chibi proportions, thick clean vector outlines, cel shading with soft gradients, vibrant neon glow, suitable as an app icon, no text, no watermark.
> 2. COMPOSITION: circular emblem / app-icon layout, main character perfectly centered and large, supporting elements arranged in a ring around her, symmetric and balanced, strong silhouette readable at 32px.
> 3. CHARACTER: a cute chibi silver-haired girl mage, AI programming assistant mascot, glowing cyan-blue eyes, twin long hair strands flowing like data streams, cyberpunk wizard outfit (dark navy magical robe with neon cyan circuit lines, small floating holographic runes around the collar, short gloves with glowing fingertip tips).
> 4. POSE & GESTURE: floating upright, both hands spread open outward in a spellcasting-guide gesture, emitting tiny glowing code particles from her fingertips, gentle confident smile.
> 5. CENTRAL PROP: a glowing holographic terminal window floating in front of her chest, translucent glass panel with a title bar of three dots, a blinking cursor and a few lines of code (characters like `>_`, `{}`, `0 1 0 1`), the window slightly tilted toward the viewer.
> 6. ENERGY FLOW: neon streams of code (0 and 1) flow out of the terminal window, curl around her hands and dissolve into star-like light particles, suggesting a two-way AI-human collaboration loop.
> 7. SUPPORTING ELEMENTS (arranged around her, small and simple): a branching silver light cable forking into two paths (session branching); a translucent energy shield with a lock icon (path safety); two or three tiny glowing minion copies of her (sub-agent swarm); each element drawn flat and readable at small size.
> 8. COLOR PALETTE: deep navy background (#2E3440), neon cyan (#88C0D0) and blue (#5E81AC) as the two accent glows, white hair highlights, pure black only for outlines, high contrast between glow and background.
> 9. BACKGROUND: dark navy with a subtle terminal-style grid pattern and faint floating dot particles, a soft radial cyan glow behind the character, no other scenery, minimal and clean.
>
> **Prompt A（英文 · 拼接版，直接粘贴使用）：**
>
> Clean flat anime illustration, chibi proportions, thick clean vector outlines, cel shading, vibrant neon glow, app icon, no text. Circular emblem layout, centered main character, supporting elements in a ring. A cute chibi silver-haired girl mage, AI programming assistant mascot, glowing cyan-blue eyes, hair strands flowing like data streams, cyberpunk wizard robe with neon circuit lines, floating upright with both hands spread in a spellcasting-guide gesture, emitting code particles from fingertips. A glowing holographic terminal window floats in front of her chest, translucent panel with three-dot title bar, blinking cursor, lines of code `>_ { } 0 1 0 1`, slightly tilted toward viewer. Neon streams of 0 and 1 flow out of the window, curl around her hands, dissolve into star particles. Around her: a branching silver light cable forking into two paths, a translucent energy shield with lock icon, two tiny glowing minion copies of her. Deep navy background (#2E3440), neon cyan (#88C0D0) and blue (#5E81AC) glows, white highlights, dark navy grid pattern with faint dots, radial cyan glow behind character. High contrast, symmetric, readable at 32px.

> **中文参考：**
>
> 干净的平涂二次元插画，Q 版比例，粗而干净的矢量描边，赛璐璐上色，霓虹辉光，应用图标风格，无文字无水印。圆形徽章构图，主人物居中放大，辅助元素环绕排列，对称平衡，小尺寸（32px）下剪影清晰可辨。主角是一只 Q 版银发少女魔法使，AI 编程助手人格化形象，青蓝色发光瞳孔，双长马尾发丝像数据流一样飘动，穿赛博朋克魔法袍（深藏青袍身 + 霓虹青色电路纹路，领口飘浮全息符文，戴发光指尖的短手套）。姿态：悬浮直立，双手向外摊开作施法引导状，指尖散出微小发光代码粒子，自信温柔的微笑。核心道具：胸前悬浮一块发光全息终端窗口，半透明玻璃面板，顶部三颗圆点标题栏，内有闪烁光标和几行代码字符（`>_`、`{}`、`0 1 0 1`），窗口微微朝观者倾斜。能量流：霓虹代码流（0 和 1）从终端涌出，绕手旋转后化作星点消散，表达 AI 与程序员双向协作。环绕辅助元素（小而简洁）：分叉成两条的银色光缆（会话分支）、带锁图标的半透明能量护盾（路径安全）、两三个迷你发光分身（子 Agent 集群）。配色：深藏青背景（#2E3440）、霓虹青（#88C0D0）与蓝（#5E81AC）双辉光、白色高光，描边纯黑，辉光与背景高对比。背景：深藏青细网格线 + 稀疏漂浮光点，人物身后柔和青色径向光晕，无其他场景，极简干净。

### 变体 B：终端黑客少女（酷炫向）

> **Prompt B（英文）：**
>
> Anime-style icon of a chibi girl hacker, an AI coding companion mascot, wearing a glowing terminal-window visor over her eyes, striking a confident pose in front of a large green-on-black command line interface. Code brackets { } and a branching silver light cable (session fork) arc around her. Small glowing shuriken-like shields orbit nearby. Style: vibrant anime key visual, cel shading, neon cyan and blue accents on dark navy, clean composition, minimal background, high contrast, app icon style, no text.

### 变体 C：治愈系办公桌少女（亲和向）

> **Prompt C（英文）：**
>
> Cute anime chibi girl mascot, an AI programming assistant, sitting at a tiny desk with a laptop showing a terminal with streaming code, a warm cup of tea beside her, and a soft glow of a heart-shaped hologram representing her AI companion bond. Soft pastel anime illustration, gentle lighting, cozy tech-office mood, keep color accents of cyan #88C0D0 and blue #5E81AC on a warm dark background, minimal clean composition, high contrast, app icon, no text.

### 变体 D：极简标志风（适配小尺寸 / 官网 favicon）

> **Prompt D（英文）：**
>
> Minimal flat vector logo, a rounded square badge with gradient from #5E81AC to #88C0D0 on dark navy, featuring a stylized terminal prompt symbol (>_) merged with a cheerful digital face made of two code brackets { } as eyes. No animal or character mascot, pure abstract tech symbol. Flat anime-influenced vector style, thick uniform stroke, high contrast, scalable to 32px, no text.

---

## 三、中文提示词（部分模型 / 国产工具适配）

> **中文通用版：**
>
> 二次元插画风格的应用图标，主角是一位 Q 版可爱的银发少女魔法使，她是 AI 编程助手，面前悬浮着发光的全息终端窗口，窗口里流淌着霓虹色的代码雨（0 和 1）。少女周围漂浮着全息小卡片：代表会话分叉的树状光缆、代表安全防护的护盾、以及一群她的迷你发光分身。整体配色为深藏青色背景，霓虹青（#88C0D0）和蓝色（#5E81AC）的光效，白色高光。平涂干净的矢量二次元风格，粗描边，居中构图，背景简洁，对比度高，适合作为应用图标，不要出现任何文字。

---

## 四、使用建议

- **推荐模型**：Midjourney v6 / v7、Stable Diffusion XL（配合 anime 风格 LoRA）、DALL·E 3、即梦 / 豆包 / 通义万相等国产工具。
- **比例**：图标使用 `1:1`（正方形）；吉祥物海报可用 `16:9`。
- **负向提示词（Negative）**：`text, watermark, signature, blurry, low quality, extra limbs, distorted, realistic photo`。
- **风格词备选**：`anime key visual`（番剧主视觉）、`chibi`（Q 版）、`flat vector`（扁平矢量）、`cel shading`（赛璐璐）、`cyberpunk`（赛博朋克）。
- **一致性**：若需系列产出，固定 Prompt 中主色 hex 与「chibi 魔法使少女 + 全息终端窗口 + 分支光缆 + 护盾」四个核心元素不变，只换风格词。若不想用人形角色，可将「少女」替换为「全息小使魔（holographic fairy minion）」，其余元素保持不变。
- **后续应用**：生成的图标可放入 `assets/` 目录，用于 README 顶部、`docs/` 文档、Docker 镜像标题等处。
