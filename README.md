# nucleagent-executor

Nucleagent 执行器。任务执行引擎，承载 OpenCode/Hermes Agent 上下文管理。

基于 [Prism Fusion](https://github.com/kwhitestone/prism-fusion) 框架构建。

## 结构

```
nucleagent-executor/
├── prism-fusion/              git submodule
├── app/
│   ├── src/
│   │   ├── server/            Go 后端
│   │   │   ├── addons/        业务插件
│   │   │   ├── go.work
│   │   │   ├── go.mod
│   │   │   ├── config.yaml
│   │   │   └── main.go
│   │   └── web/               Vue 前端 (micro-app 子应用)
│   └── Dockerfile
└── README.md
```

## 端口

- 后端: 6690
- 前端: 6698
