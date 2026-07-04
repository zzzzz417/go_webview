# PLAN

## 1. 项目目标

本项目的目标是把原始的三个 Python 抓包脚本能力：

- `getList.py`：查询某一天可预约场地与时间段
- `req.py`：构造预约下单请求，可包含多个时间段
- `pay.py`：根据下单返回的 `recordId` 发起支付

整合成一个可直接使用的自动预约应用，并最终打包成轻量级桌面程序。

项目后续又进一步演进为两层结构：

- Python 负责认证、预约业务、轮询、支付、配置持久化
- C++ + WebView 负责轻量桌面外壳和窗口承载

因此当前项目同时包含：

- 原始接口验证脚本
- Python 后端服务
- HTML/CSS/JS 前端
- C++ 启动器与打包产物

---

## 2. 目录说明

项目根目录的主要文件：

- [getList.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/getList.py)
    - 原始查询脚本
- [req.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/req.py)
    - 原始下单脚本
- [pay.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/pay.py)
    - 原始支付脚本
- [bag.fbs](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/bag.fbs)
    - 抓包记录
- [app.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app.py)
    - 旧版 Python GUI 原型
- [PLAN.md](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/PLAN.md)
    - 本说明文档

`app/` 目录是当前主实现：

- [app/auth.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/auth.py)
    - 登录认证与 `get_kjyy_token`
- [app/backend_service.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/backend_service.py)
    - 核心业务层
- [app/api_server.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/api_server.py)
    - Flask HTTP 服务层
- [app/frontend/index.html](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/frontend/index.html)
    - 前端页面结构
- [app/frontend/styles.css](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/frontend/styles.css)
    - 前端样式
- [app/frontend/app.js](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/frontend/app.js)
    - 前端交互逻辑
- [app/native_shell/src/main.cpp](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/native_shell/src/main.cpp)
    - C++ WebView 启动器
- [app/build_release.ps1](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/build_release.ps1)
    - 一键打包脚本
- [app/release/DGUTTennisAutoBooker](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/release/DGUTTennisAutoBooker)
    - 当前发布目录
- [app/release/DGUTTennisAutoBooker.zip](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/release/DGUTTennisAutoBooker.zip)
    - 当前发布压缩包

---

## 3. 原始接口链路

### 3.1 查询接口

查询接口来自原始 `getList.py`，核心用途是获取某一天所有网球场及其可预约时段。

关键参数：

- `params[meetDate]`
    - 指定查询日期

返回结果中，每个场地会带一组时间段，每个时间段都有状态值。

当前系统中，这部分能力被收敛到：

- `BookingClient.fetch_day()`
- `ReservationService.refresh_week()`

### 3.2 下单接口

原始 `req.py` 用于构造预约请求。

特点：

- 可以一次携带多个连续或多个选中的时间段
- 如果下单成功，返回体中的 `data` 为订单号 `recordId`

当前系统中，这部分能力被收敛到：

- `BookingClient.initiate()`

### 3.3 支付接口

原始 `pay.py` 用于支付已经创建好的订单。

当前系统中，这部分能力被收敛到：

- `BookingClient.pay()`

### 3.4 自动化闭环

现在完整链路为：

1. 登录获取 token
2. 查询开放日期的场地信息
3. 命中用户配置的目标
4. 发起下单
5. 拿到 `recordId`
6. 自动支付
7. 将目标标记为 `purchased`

---

## 4. 当前系统架构

当前采用“轻量混合架构”：

### 4.1 业务后端：Python

后端负责：

- 登录与 token 获取
- token 自动刷新
- 查询一周场地数据
- 维护选中模式
- 根据规则轮询开放目标
- 自动下单与支付
- 日志输出
- 配置持久化

核心文件：

- [app/backend_service.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/backend_service.py)
- [app/auth.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/auth.py)

### 4.2 接口层：Flask

Flask 负责把 Python 业务层暴露为本地 HTTP 服务。

核心文件：

- [app/api_server.py](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/api_server.py)

作用：

- 提供 `/api/*` 接口给前端调用
- 提供静态页面文件
- 允许被单独打成 `kjyy_api.exe`

### 4.3 前端：HTML/CSS/JS

前端不是 Qt 控件，而是标准网页界面。

特点：

- 登录页与主界面分离
- 主界面与设置页分离
- 左边为三维表的一层视图
    - 先选场地
    - 再展示“周一到周日 × 时间段”的二维勾选表
- 右边显示已选择项、状态、运行日志

核心文件：

- [app/frontend/index.html](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/frontend/index.html)
- [app/frontend/styles.css](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/frontend/styles.css)
- [app/frontend/app.js](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/frontend/app.js)

### 4.4 桌面壳：C++ + WebView

桌面壳负责：

- 启动本地 Python 后端
- 检测后端是否就绪
- 打开 WebView 窗口承载前端页面
- 关闭窗口时回收当前启动的后端进程

核心文件：

- [app/native_shell/src/main.cpp](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/native_shell/src/main.cpp)

当前采用本地 HTTP + WebView 模式，而不是 PySide6 全量 GUI，因此内存占用和包体更轻。

---

## 5. 登录与认证设计

### 5.1 登录方式

应用打开后首先展示登录页，要求用户输入：

- 账号
- 密码

登录成功条件：

- `auth.get_kjyy_token(...)` 成功返回 token

### 5.2 token 设计

系统内部保存：

- 原始 token
- `Bearer {token}` 形式的认证头

前端设置页中：

- token 只读展示
- Bearer 认证串只读展示
- 不允许用户直接手改

### 5.3 自动续期

当出现以下情况之一时，认为 token 可能失效：

- 接口返回 `401/403`
- 返回消息中含有认证失败、登录失效等语义
- 查询数据为空且表现像鉴权失效

此时后端会尝试：

1. 使用已保存账号密码重新 `login`
2. 刷新 token
3. 自动重试请求

对应实现：

- `AuthManager.refresh_token()`
- `BookingClient._request_json()`
- `BookingClient._request_day()`

### 5.4 本地持久化

系统会保存：

- 账号
- 密码
- token
- 周起始日期
- 模式设置
- 手动日期
- 售罄策略

优先保存位置：

- `%APPDATA%/DGUTTennisAutoBooker/config.json`

回退位置：

- [app/.local_data/config.json](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/.local_data/config.json)

---

## 6. 预约规则设计

### 6.1 用户确认的基础规则

规则核心不是“只有到 08:00 才能开始整体预约”，而是：

- 某个日期只有在它的开放时间之后，才允许对这个日期发起预约尝试

系统中的开放规则为：

- 某个目标日期 `D`
- 它的开放时间为 `D - 6 天` 的早上 `08:00`

也就是说：

- 周二 08:00 开放下周一
- 周三 08:00 开放下周二
- 以此类推

代码常量：

- `OPEN_ADVANCE_DAYS = 6`
- `OPEN_TIME = 08:00`

### 6.2 常规轮询

在非抢票高频期，对“已经开放”的目标执行安全轮询：

- 单次间隔不低于 `15` 秒
- 任意 `30` 分钟内请求不超过 `10` 次

对应常量：

- `MIN_RETRY_INTERVAL_SECONDS = 15`
- `MAX_ATTEMPTS_PER_30_MINUTES = 10`
- `RATE_LIMIT_WINDOW = 30 分钟`

### 6.3 08:00 新开放高频阶段

每天 `08:00 - 08:10` 之间：

- 只对“刚开放的新日期”且在用户已选目标中的项目进入高频
- 普通已开放但不是刚开放的目标，不进入高频

当前高频参数：

- 高频窗口：`10` 分钟
- 高频间隔：`3` 秒
- 高频窗口最大尝试数：`200`

对应常量：

- `HIGH_FREQ_WINDOW_MINUTES = 10`
- `HIGH_FREQ_INTERVAL_SECONDS = 3`
- `HIGH_FREQ_MAX_ATTEMPTS = 200`

### 6.4 售罄处理

当某个目标连续失败达到阈值后，标记为 `sold_out`。

当前阈值：

- `FAILURES_TO_MARK_SOLD_OUT = 2`

售罄策略可配置：

- `disable`
    - 完全关闭该目标，直到下一个新的开放周期
- `cooldown`
    - 降频补漏

当前低频补漏间隔：

- `LOW_PRIORITY_INTERVAL_SECONDS = 90`

### 6.5 已购处理

如果某目标成功购买：

- 状态标记为 `purchased`
- 暂停到下一周对应的新开放周期

---

## 7. 选择模型设计

### 7.1 三维表的含义

用户最终要表达的是三维选择：

- 场地
- 周几
- 时间段

UI 上的实现方式是分层展开：

1. 先选一个场地
2. 再看到该场地下的二维表
    - 横轴：周一到周日
    - 纵轴：时间段
3. 每个格子用复选框表示是否要抢

因此底层存储虽然是三维信息，展示时是一组“按场地切换的二维矩阵”。

### 7.2 选择项存储

单个选择项的数据结构：

- `weekday`
- `site_id`
- `site_name`
- `start_hm`
- `end_hm`

对应类：

- `SelectionPattern`

唯一键格式：

- `siteId|weekday|startHm`

例如：

- `239|0|19:00`

---

## 8. 状态机设计

当前前端显示的状态主要有五种：

- `pending_release`
    - 即将开始新的售卖
- `polling`
    - 轮询补漏
- `high_priority`
    - 正在抢票
- `sold_out`
    - 售罄
- `purchased`
    - 已购买

这些状态由 `PatternRuntimeState` 驱动。

状态切换逻辑大致为：

1. 目标未开放
    - `pending_release`
2. 目标已开放且处于 08:00-08:10 新开放高频期
    - `high_priority`
3. 目标已开放但非高频期
    - `polling`
4. 连续失败达到阈值
    - `sold_out`
5. 成功支付
    - `purchased`

---

## 9. 运行中禁止修改的规则

用户明确要求：

- 只要某操作会影响预约程序进行，就不允许在运行时修改

当前被禁止的典型修改包括：

- 新增或删除已选时段
- 清空选择
- 修改模式
- 修改手动日期
- 修改售罄策略

统一返回提示：

- `请先停止预约程序，再进行更改`

对应实现：

- `ReservationService.blocked_response()`

---

## 10. 页面与交互流程

### 10.1 启动流程

1. 双击 `DGUTTennisAutoBooker.exe`
2. C++ 启动器拉起 `kjyy_api.exe`
3. 等待本地 `/api/ping` 就绪
4. 打开 WebView
5. 加载前端登录页

### 10.2 登录页

登录页只做一件事：

- 输入账号密码，点击确认登录

登录成功后：

- 自动切换到主界面

### 10.3 主界面

主界面包含：

- 左侧场地矩阵
- 右侧已选项目
- 模式切换
- 启停按钮
- 实时日志

### 10.4 设置页

设置页包含：

- 账号只读显示
- User-Agent 只读显示
- token 只读显示
- Bearer 串只读显示
- 售罄策略切换
- 手动刷新 token
- 重新登录

---

## 11. API 设计

本地服务基地址：

- `http://127.0.0.1:<动态端口>`

优先端口：

- `18765`

若被占用：

- 在 `18765 ~ 18810` 中顺延寻找可用端口

主要接口：

- `GET /api/ping`
- `GET /api/state`
- `POST /api/login`
- `POST /api/token/refresh`
- `POST /api/relogin`
- `POST /api/week`
- `POST /api/site`
- `POST /api/selection/toggle`
- `POST /api/selection/remove`
- `POST /api/selection/clear`
- `POST /api/settings/mode`
- `POST /api/settings/manual-date`
- `POST /api/settings/sold-out-strategy`
- `POST /api/booking/start`
- `POST /api/booking/stop`

详细示例见：

- [app/API_ROUTES.md](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/API_ROUTES.md)

---

## 12. 打包方案

### 12.1 Python 后端

打包方式：

- `PyInstaller --onefile`

产物：

- `kjyy_api.exe`

关键点：

- 将 `frontend/` 目录作为数据文件一并打入
- 运行时通过 `sys._MEIPASS` 定位资源

### 12.2 C++ 外壳

打包方式：

- `CMake + MinGW`

产物：

- `DGUTTennisAutoBooker.exe`

依赖：

- `WebView2Loader.dll`
- `libgcc_s_seh-1.dll`
- `libstdc++-6.dll`
- `libwinpthread-1.dll`

### 12.3 发布目录

当前发布目录：

- [app/release/DGUTTennisAutoBooker](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/release/DGUTTennisAutoBooker)

典型内容：

- `DGUTTennisAutoBooker.exe`
- `kjyy_api.exe`
- `WebView2Loader.dll`
- MinGW 运行时 DLL
- `PACKAGE_README.txt`

### 12.4 一键打包脚本

脚本：

- [app/build_release.ps1](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/build_release.ps1)

作用：

1. 重打 `kjyy_api.exe`
2. 重编 `DGUTTennisAutoBooker.exe`
3. 复制运行时 DLL
4. 生成 `release/` 目录
5. 压缩为 zip

---

## 13. 启动器特殊处理

为了适配当前项目路径中存在中文目录名，启动器做了几项特殊处理：

- 启动日志写入 `launcher.log`
- 使用 PowerShell 脚本拉起 `kjyy_api.exe`
- 启动脚本使用带 BOM 的 UTF-16 LE 编码写出
- 后端退出时按完整路径回收对应 `kjyy_api.exe`

日志文件位置：

- [app/release/DGUTTennisAutoBooker/launcher.log](/C:/Users/zhaozhao/Desktop/uschool/uuuu/测试/app/release/DGUTTennisAutoBooker/launcher.log)

该日志用于排查：

- 后端启动失败
- 端口冲突
- 启动等待超时
- WebView 打开失败

---

## 14. 当前已完成内容

已经完成的核心能力：

- 原始查询、下单、支付接口已打通
- 登录成功后可自动获取 token
- token 可自动刷新
- 三维选择模型已建立
- UI 已拆成登录页 / 主界面 / 设置页
- 已支持运行时锁定修改
- 已支持高频抢刚开放目标
- 已支持常规安全轮询
- 已支持售罄关闭 / 降频两种策略
- 已支持自动支付
- 已完成轻量版打包

---

## 15. 当前已知问题

### 15.1 源码文件存在部分编码污染

当前一些源码文件里能看到中文乱码，例如：

- `frontend/index.html`
- `backend_service.py`
- `native_shell/src/main.cpp`

这不一定影响运行，但会影响后续维护与继续开发。

建议后续统一：

- 全部转为 UTF-8
- 修正所有中文文案

### 15.2 Flask 当前仍是开发服务器

当前 `api_server.py` 用的是 Flask 自带 `app.run(...)`。

优点：

- 简单
- 打包方便

缺点：

- 不是严格意义上的生产级 WSGI 服务

不过在本地桌面应用场景下通常可以接受。

### 15.3 启动器仍依赖 PowerShell

当前启动器最稳的方式是通过 PowerShell 启动 `kjyy_api.exe`。

这意味着：

- Windows 环境是默认前提
- 若目标环境 PowerShell 受限，需要额外验证

---

## 16. 后续建议

建议的后续工作优先级如下：

### P1

- 统一修复项目中文编码
- 清理 UI 中文文案乱码
- 补一份真正面向用户的 `README`

### P2

- 把高频参数、阈值参数完全暴露到设置页
- 增加“高价值票”的显式配置
- 增加更细的状态提示与失败原因

### P3

- 为关键业务逻辑补最基本的自动化测试
- 为启动器补更多异常日志
- 进一步减小 Python 打包体积

---

## 17. 运行流程总结

最终用户的使用过程应为：

1. 启动 `DGUTTennisAutoBooker.exe`
2. 自动拉起本地后端
3. 进入登录页
4. 输入账号密码登录
5. 拉取一周场地数据
6. 选择场地、周几、时间段
7. 点击开始预约
8. 系统根据开放时间、安全轮询规则与高频窗口自动下单和支付
9. 在日志区查看进度
10. 如需修改配置，先停止预约程序

---

## 18. 一句话总结

这个项目当前已经从“单纯抓包脚本集合”，演进成了一个：

- 具备登录、查场、选场、自动抢票、自动支付能力
- 采用 Python 业务后端 + HTML 前端 + C++ 轻量桌面壳
- 可以直接打包交付使用

的完整自动预约系统。
