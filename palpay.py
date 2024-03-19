
import requests
import json
import uuid  

paypal_sandbox_url = "https://api.sandbox.paypal.com/v1/oauth2/token"
client_id = ""
secret = ""

paypal_request_id = str(uuid.uuid4())

payload = {
    "grant_type": "client_credentials"
}

headers = {
    "Content-Type": "application/x-www-form-urlencoded",
    "PayPal-Request-Id": paypal_request_id
}

response = requests.post(
    paypal_sandbox_url,
    auth=(client_id, secret),
    headers=headers,
    data=payload
)

response_data = json.loads(response.text)

access_token = response_data.get("access_token")

if access_token:
    print(f"PayPal Access Token: {access_token}")
    order_url = "https://api.sandbox.paypal.com/v2/checkout/orders"
    order_payload = {
        "intent": "CAPTURE",
        "purchase_units": [
            {
                "amount": {
                    "currency_code": "USD",
                    "value": "10.00"
                }
            }
        ],
        "application_context": {
            "cancel_url": "http://xxxx",
            "return_url": "http://xxxx"
        }
    }
    uid=str(uuid.uuid4())
    print(uid)
    order_headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {access_token}",
        "PayPal-Request-Id": uid  
    }

    order_response = requests.post(
        order_url,
        headers=order_headers,
        json=order_payload
    )
    order_response_data = json.loads(order_response.text)
    print("Order Response:")
    print(json.dumps(order_response_data, indent=2))
else:
    print("Failed to get PayPal Access Token.")
    print(f"Error: {response_data.get('error')}, Description: {response_data.get('error_description')}")
'''
会返回这样的数据
PayPal Access Token: token
3076fcec-eef9-4455-a363-d3c15b509d6c
Order Response:
{
  "id": "xxxxxx",
  "status": "CREATED",
  "links": [
    {
      "href": "https://api.sandbox.paypal.com/v2/checkout/orders/xxxxxx",
      "rel": "self",
      "method": "GET"
    },
    {
      "href": "https://www.sandbox.paypal.com/checkoutnow?token=xxxxxx",
      "rel": "approve",
      "method": "GET"
    },
    {
      "href": "https://api.sandbox.paypal.com/v2/checkout/orders/xxxxxx",
      "rel": "update",
      "method": "PATCH"
    },
    {
      "href": "https://api.sandbox.paypal.com/v2/checkout/orders/xxxxxx/capture",
      "rel": "capture",
      "method": "POST"
    }
  ]
}
取https://www.sandbox.paypal.com/checkoutnow?token=xxxxxx"也就是approve返回给用户用于授权支付,用户在此界面后进入paypal界面进行授权，点击继续订单后跳入return_url，个人建议在return加入&参数=？？？用于前端处理
https://api.sandbox.paypal.com/v2/checkout/orders/"+ xxxxxx + "/capture用于完成支付POST请求，添加头->Authorization: Bearer token以及常见的Content-Type: application/json'''