import json
import time
import jwt  # PyJWT库
import requests

def get_access_token(service_account_file):
    # 读取服务帐户的 JSON 文件

    # 提取关键信息
    private_key = service_account_info['private_key']
    client_email = service_account_info['client_email']
    
    # 构造JWT的声明（Claim）
    now = int(time.time())
    jwt_headers = {
        "alg": "RS256",
        "typ": "JWT"
    }
    payload = {
        "iss": client_email,  # 发行者，来自服务帐户的电子邮件
        "scope": "https://www.googleapis.com/auth/firebase.messaging",  # 使用的权限范围
        "aud": "https://oauth2.googleapis.com/token",  # 目标地址
        "iat": now,  # 签发时间
        "exp": now + 3600  # 过期时间（1小时后）
    }
    
    # 使用私钥生成JWT
    jwt_token = jwt.encode(payload, private_key, algorithm="RS256", headers=jwt_headers)
    print(jwt_token)    
    # 构造用于获取访问令牌的POST请求
    token_url = "https://oauth2.googleapis.com/token"
    token_headers = {
        "Content-Type": "application/x-www-form-urlencoded"
    }
    token_data = {
        "grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer",
        "assertion": jwt_token
    }
    
    # 发送请求获取访问令牌
    response = requests.post(token_url, headers=token_headers, data=token_data)
    
    # 检查响应并返回令牌
    if response.status_code == 200:
        access_token = response.json()['access_token']
        return access_token
    else:
        raise Exception(f"Error getting access token: {response.text}")

# 示例调用
service_account_file = 'serviceAccountKey.json'  # 替换为你的文件路径
access_token = get_access_token(service_account_file)
print("Access Token:", access_token)
