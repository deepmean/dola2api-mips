## msToken生成算法
#/*
# * Copyright AngelToms
# * SPDX-License-Identifier: Apache-2.0
# */

import requests
import time

## strData 每个客户端不一样，它是浏览器相关指纹，但一般固定客户端不轻易发生变化，可以给一个默认值
## strData详细生成过程请详见：https://www.bilibili.com/video/BV1CdKC6LEGH/?spm_id_from=333.1387.homepage.video_card.click
def get_msToken(appid, strData):
    # url = "https://mssdk.bytedance.com/web/r/token?ms_appid=6383"
    # url = "https://mssdk.bytedance.com/web/r/token?ms_appid=2385"

    # 同样的效果
    # https://mssdk.bytedance.com/web/common?ms_appid=6383
    # 这里实现为https://mssdk.bytedance.com/web/r/token
    if appid is not None:
        url = "https://mssdk.bytedance.com/web/r/token?ms_appid=" + appid
    else:
        url = "https://mssdk.bytedance.com/web/r/token?ms_appid=6383"
    

    payload = {
        "magic": 0x20200422,
        "version": 1,
        "dataType": 8, # WEB_DEVICE_INFO = 8
        "strData": strData,
        "tspFromClient": int(time.time() * 1000), # js代码：new Date()['getTime']()
        "ulr": 0
    }

    headers = { # 必须包含Referer
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36",
        "Referer": "https://rmc.bytedance.com/"
    }

    response = requests.post(url, json=payload, headers=headers)
    # print(response.text)
    # print(response.cookies)
    msToken = response.cookies.get('msToken')
    print(f"msToken: {msToken}")
    return msToken

def update_msToken(appid, strData, mstoken, ttwid):
    # 同样的效果
    # https://mssdk.bytedance.com/web/r/token .....
    # 这里实现为https://mssdk.bytedance.com/web/common?ms_appid=6383 .....
    
    if appid is not None:
        url = "https://mssdk.bytedance.com/web/common?ms_appid=" + appid + "&msToken=" + mstoken
    else:
        url = "https://mssdk.bytedance.com/web/common?ms_appid=6383&msToken=" + mstoken

    payload = {
        "magic": 0x20200422,
        "version": 1,
        "dataType": 8, # WEB_DEVICE_INFO = 8
        "strData": strData,
        "tspFromClient": int(time.time() * 1000), # js代码：new Date()['getTime']()
        "ulr": 0
    }

    headers = { # 必须包含Referer
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36",
        "Referer": "https://rmc.bytedance.com/"
    }

    cookies = {
        "ttwid":f"{ttwid}"
    }

    response = requests.post(url, json=payload, headers=headers, cookies=cookies)
    # print(response.text)
    # print(response.cookies)
    msToken = response.cookies.get('msToken')
    print(f"msToken: {msToken}")
    return msToken


#此方法可以直接获取，但是可能会被封掉
def get_ttwid0():
    url = "https://ttwid.bytedance.com/ttwid/union/register/"

    payload = {
        "region": "cn",
        "aid": 1768,
        "needFid": False,
        "service": "www.ixigua.com",
        "migrate_info": {"ticket": "", "source": "node"},
        "cbUrlProtocol": "https",
        "union": True
    }

    headers = {
        'Content-Type': 'application/json'
    }

    response = requests.post(url, json=payload, headers=headers)
    ttwid = response.cookies.get('ttwid')
    print(f"ttwid: {ttwid}")
    return ttwid

# 这个可以通过输入任意url获取ttwid，就像正常访问一样，只要切换url接口，几乎不会导致被封
default_url = "https://live.douyin.com/646454278948"
def get_ttwid(in_url):

    if (in_url == ""):
        url = default_url
    else :
        url = in_url
        
    headers = {
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
    }
    
    response = requests.get(url, headers=headers)
    ac_nonce = response.cookies.get("__ac_nonce")
    # print (f"__ac_nonce:{ac_nonce}")

    cookies = {
        "__ac_nonce": ac_nonce
    }

    response = requests.get(url, headers=headers, cookies=cookies)
    ttwid = response.cookies.get_dict()['ttwid']
    print(f"ttwid: {ttwid}")
    return ac_nonce, ttwid