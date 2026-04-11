#!/bin/bash

PROXY_ADDR="http://127.0.0.1:38457"
SSH_CONFIG_FILE="/home/pooky/.ssh/config"

# 检查 root 权限
if [ "$EUID" -ne 0 ]; then 
    echo "❌ 请使用 sudo 运行此脚本: sudo $0 [start|stop]"
    exit 1
fi

show_usage() {
    echo "用法: sudo $0 [start|stop]"
    echo ""
    echo "命令:"
    echo "  start  启动 FastGitHub 服务并配置 Git/SSH 代理"
    echo "  stop   停止 FastGitHub 服务并取消 Git/SSH 代理"
    exit 1
}

# 备份 SSH 配置
backup_ssh_config() {
    if [ -f "$SSH_CONFIG_FILE" ]; then
        cp "$SSH_CONFIG_FILE" "${SSH_CONFIG_FILE}.backup.$(date +%Y%m%d_%H%M%S)"
    fi
}

# 检查并安装 corkscrew
ensure_corkscrew() {
    if ! command -v corkscrew &> /dev/null; then
        echo "📦 正在安装 corkscrew..."
        apt update && apt install -y corkscrew || yum install -y corkscrew
        if [ $? -ne 0 ]; then
            echo "❌ corkscrew 安装失败，SSH 代理将无法配置"
            exit 1
        fi
    fi
}

start_service() {
    echo "🚀 正在启动 FastGitHub 服务..."
    
    if systemctl start fastgithub.service; then
        echo "✅ FastGitHub 服务启动命令已发送"
        
        sleep 3
        
        if systemctl is-active --quiet fastgithub.service; then
            echo "✅ 服务状态：运行中"
            
            if pgrep -f "dnscrypt-proxy" > /dev/null; then
                echo "✅ DNS 代理进程正常"
            else
                echo "⚠️  警告：DNS 代理进程未找到"
                journalctl -u fastgithub -n 5
            fi
        else
            echo "❌ 服务启动失败"
            journalctl -u fastgithub -n 10
            exit 1
        fi
    else
        echo "❌ systemctl start 执行失败"
        exit 1
    fi

    # 配置 Git 代理
    echo "🔧 正在配置 Git 全局代理..."
    git config --global http.proxy "$PROXY_ADDR"
    git config --global https.proxy "$PROXY_ADDR"
    echo "✅ Git HTTP/HTTPS 代理已配置"

    # 配置 SSH 代理
    echo "🔧 正在配置 SSH 代理..."
    ensure_corkscrew
    
    backup_ssh_config
    
    # 创建 SSH 配置目录（如果不存在）
    mkdir -p "$(dirname "$SSH_CONFIG_FILE")"
    
    # 检查是否已存在 github.com 配置
    if grep -q "Host github.com" "$SSH_CONFIG_FILE" 2>/dev/null; then
        echo "⚠️  发现已存在的 github.com 配置，将覆盖"
        # 删除旧的 github.com 配置块
        sed -i '/^Host github.com$/,/^$/d' "$SSH_CONFIG_FILE"
    fi
    
    # 添加新的 SSH 代理配置
    cat >> "$SSH_CONFIG_FILE" <<EOF
Host github.com
  Hostname github.com
  User git
  Port 22
  ProxyCommand corkscrew 127.0.0.1 38457 %h %p

EOF
    
    echo "✅ SSH 代理配置已写入: $SSH_CONFIG_FILE"

    # 测试连接
    echo "🧪 正在测试代理..."
    if timeout 10s curl -x "$PROXY_ADDR" -s https://github.com > /dev/null; then
        echo "✅ HTTP 代理测试成功"
    else
        echo "⚠️  HTTP 代理测试超时"
    fi
    
    echo "🎉 全部完成！"
    echo "💡 提示：Git SSH 地址现在也可使用，如: git clone git@github.com:user/repo.git"
}

stop_service() {
    echo "🛑 正在关闭 FastGitHub 服务..."
    
    if systemctl stop fastgithub.service; then
        echo "✅ FastGitHub 服务停止命令已发送"
        
        sleep 2
        if systemctl is-active --quiet fastgithub.service; then
            echo "⚠️  警告：服务仍在运行"
            systemctl status fastgithub.service
        else
            echo "✅ 服务状态：已停止"
        fi
    else
        echo "⚠️  FastGitHub 服务可能未在运行"
    fi

    # 取消 Git 代理
    echo "🔧 正在取消 Git 全局代理..."
    git config --global --unset http.proxy 2>/dev/null && echo "✅ Git HTTP 代理已移除"
    git config --global --unset https.proxy 2>/dev/null && echo "✅ Git HTTPS 代理已移除"

    # 取消 SSH 代理
    echo "🔧 正在清理 SSH 代理配置..."
    if [ -f "$SSH_CONFIG_FILE" ]; then
        backup_ssh_config
        
        if grep -q "ProxyCommand corkscrew 127.0.0.1 38457" "$SSH_CONFIG_FILE"; then
            # 删除 FastGitHub 添加的 github.com 配置块
            sed -i '/^Host github.com$/,/^ProxyCommand corkscrew 127.0.0.1 38457 %h %p$/d' "$SSH_CONFIG_FILE"
            
            # 清理可能的空行
            sed -i '/^$/N;/^\n$/D' "$SSH_CONFIG_FILE"
            
            echo "✅ SSH 代理配置已清理"
        else
            echo "ℹ️  SSH 配置中未发现 FastGitHub 代理设置"
        fi
    else
        echo "ℹ️  SSH 配置文件不存在: $SSH_CONFIG_FILE"
    fi

    echo "🧪 验证 Git 配置状态..."
    git config --global --list | grep -E "http\.proxy|https\.proxy" || echo "✅ 确认：所有 Git 代理已清除"
    
    echo "👋 已恢复原始状态"
    echo "💡 提示：SSH 连接已恢复直连，如需加速请重新启动"
}

# 主逻辑
case "$1" in
    start)
        start_service
        ;;
    stop)
        stop_service
        ;;
    *)
        show_usage
        ;;
esac

