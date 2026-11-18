import os
import json
import datetime
import gradio as gr

import subprocess
import threading
import time
import datetime
from queue import Queue, Empty
from appium.webdriver.common.appiumby import AppiumBy
from appium_flutter_finder.flutter_finder import FlutterElement, FlutterFinder
from appium import webdriver
from appium.options.common import AppiumOptions
import cv2
import numpy as np
from io import BytesIO
import os
from PIL import Image
from PIL import ImageFilter
import pytesseract
import json
import sys
import requests
from flask import Flask, request
from flask_cors import CORS
import socket
import paramiko
from selenium import webdriver
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
pytesseract.pytesseract.tesseract_cmd = r'C:\Program Files\Tesseract-OCR\tesseract.exe'

options = AppiumOptions()
options.set_capability("platformName", "Android") 
options.set_capability("deviceName", "M2103K19C")
options.set_capability("app", r"D:\data\qxhl\flutter_qxhl\antu\flutter_antu\build\app\outputs\apk\debug\app-debug.apk") 
options.set_capability("automationName", "Flutter") 
options.set_capability("appium:skipDeviceInstallation", False) 
options.set_capability("appium:noReset", True) # 不重置应用状态 # 权限和应用状态配置 
options.set_capability("autoGrantPermissions", True) # 禁用自动授权 
options.set_capability("appium:noReset", False) # 允许应用状态重置 
options.set_capability("appium:fullReset", False) # 禁用完全重置 
options.set_capability("appium:dontStopAppOnReset", True) # 添加这行-重置时不停止应用 
options.set_capability("appium:disableHiddenApiPolicyCommand", True) 
options.set_capability("newCommandTimeout", 600) 
options.set_capability('unicodeKeyboard', True) 
options.set_capability('resetKeyboard', True) 
options.set_capability("adbExecTimeout", 300000); 
options.set_capability("ignoreHiddenApiPolicyErrors", True); 
options.set_capability("allowHiddenApi", True);

def normalize_path(path: str) -> str:
    """
    将 Windows 风格路径（含反斜杠或引号）统一转换为 OpenCV / Pillow 兼容的格式。
    例如:
        输入:  "D:\\ocr_screenshots\\image\\xxx.png"
        输出:  "D:/ocr_screenshots/image/xxx.png"
    """
    if not isinstance(path, str):
        return path
    # 去掉多余引号
    path = path.strip('"').strip("'")
    # 统一替换为正斜杠
    path = path.replace("\\", "/")
    return path

# ============================== OCR分析 ==============================
def ocr_screen_analysis(driver, keywords, lang='chi_sim', save_dir='d:/ocr_screenshots'):
    os.makedirs(save_dir, exist_ok=True)
    timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
    screenshot_path = os.path.join(save_dir, f'ocr_{timestamp}.png')
    
    try:
        driver.save_screenshot(screenshot_path)
        img = Image.open(screenshot_path)
        img = img.filter(ImageFilter.SHARPEN)
        img = img.convert('L')
        img = img.point(lambda x: 0 if x < 200 else 255)
        
        text = pytesseract.image_to_string(img, lang=lang, config='--psm 6 --oem 3 -c preserve_interword_spaces=1')
        print(f"【OCR识别结果】识别到{len(text)}个字符")
        print(text)
        
        matches = {keyword: keyword in text for keyword in keywords}
        return {
            'screenshot': screenshot_path,
            'text': text,
            'matches': matches
        }
    except Exception as e:
        print(f"OCR分析失败: {str(e)}")
        return {'error': str(e)}

# ============================== 设备操作 ==============================
def get_connected_device_id():
    try:
        result = subprocess.check_output("adb devices", shell=True).decode()
        lines = result.strip().split("\n")[1:]
        for line in lines:
            if line.strip().endswith("device"):
                return line.split()[0]
    except Exception as e:
        print(f"❌ 获取设备ID失败: {e}")
    return None

def adb_tap(x, y, device_id=get_connected_device_id()):
    cmd = f"adb -s {device_id} shell input tap {x} {y}"
    subprocess.run(cmd, shell=True)

# ============================== 图像匹配点击 ==============================
def match_and_tap_center(driver, finder, target_image_path, threshold=0.8):
    screenshot_path = "temp_screen.png"
    driver.save_screenshot(screenshot_path)

    screenshot = cv2.imread(screenshot_path)
    target = cv2.imread(target_image_path)

    if screenshot is None or target is None:
        print("❌ 无法读取截图或目标图片")
        return None

    screenshot_gray = cv2.cvtColor(screenshot, cv2.COLOR_BGR2GRAY)
    target_gray = cv2.cvtColor(target, cv2.COLOR_BGR2GRAY)

    result = cv2.matchTemplate(screenshot_gray, target_gray, cv2.TM_CCOEFF_NORMED)
    _, max_val, _, max_loc = cv2.minMaxLoc(result)

    print(f"🔍 匹配相似度: {max_val:.4f}")

    if max_val >= threshold:
        h, w = target.shape[:2]
        center_x = max_loc[0] + w // 2
        center_y = max_loc[1] + h // 2
        print(f"✅ 匹配成功，中心点: ({center_x}, {center_y})")

        cv2.rectangle(screenshot, max_loc, (max_loc[0] + w, max_loc[1] + h), (0, 255, 0), 2)
        cv2.imwrite("match_debug.png", screenshot)

        device_id = get_connected_device_id()
        if device_id:
            adb_tap(center_x, center_y, device_id)
        else:
            print("❌ 无法点击，未找到设备")

        return (center_x, center_y)
    else:
        print("❌ 匹配失败")
        return None

# ============================== 日志捕获 ==============================
def capture_logs(event_key, after=5):
    if event_key not in event_timestamp:
        print(f"⚠️ 无法找到事件'{event_key}'的时间戳")
        return []

    base_time = event_timestamp[event_key]
    start_time = base_time
    end_time = base_time + after

    EXCLUDED_KEYWORDS = {'GetRunInfo', 'GetUiFilePath', 'GetExpireImei', 'GetLangFile', 'CheckVersion'}
    INCLUDE_KEYWORDS = ['参数', 'error_message']

    adb_command = ["adb", "logcat", "-v", "time", "-b", "main"]

    logcat_process = subprocess.Popen(
        adb_command,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        encoding='utf-8',
        errors='ignore',
        universal_newlines=True
    )

    logs_queue = Queue()
    logs_result = []

    def output_reader(process, queue):
        for line in process.stdout:
            if line:
                queue.put(line.strip())

    reader_thread = threading.Thread(target=output_reader, args=(logcat_process, logs_queue))
    reader_thread.daemon = True
    reader_thread.start()

    deadline = time.time() + after + 3
    while time.time() < deadline:
        try:
            line = logs_queue.get(timeout=0.1)
            logs_result.append(line)
        except Empty:
            continue

    logcat_process.terminate()
    reader_thread.join(timeout=2)

    filtered_logs = []
    current_year = datetime.datetime.now().year

    for line in logs_result:
        if any(k in line for k in INCLUDE_KEYWORDS) and not any(ek in line for ek in EXCLUDED_KEYWORDS):
            try:
                time_str = f"{current_year}-{line[:18]}"
                log_time = datetime.datetime.strptime(time_str, '%Y-%m-%d %H:%M:%S.%f').timestamp()
                if start_time <= log_time <= end_time:
                    filtered_logs.append(line)
            except Exception as e:
                print(f"时间解析失败: {line[:18]}，错误: {e}")

    return filtered_logs


def click_element_by_value_key(driver, finder, value_key, wait_time=1):
    try:
        before_click = time.time()
        event_timestamp[value_key] = before_click
        element_finder = finder.by_value_key(value_key)
        element = FlutterElement(driver, element_finder)
        element.click()
        time.sleep(wait_time)
        return True
    except Exception as e:
        print(f"❌ 点击'{value_key}'失败: {str(e)}")
        return False

def click_element_by_text(driver, finder, text, wait_time=1):
    """通过文本点击元素"""
    try:
        element_finder = finder.by_text(text)
        element = FlutterElement(driver, element_finder)
        element.click()
        time.sleep(wait_time)
        return True
    except Exception as e:
        print(f"❌ 点击'{text}'失败: {str(e)}")
        return False

def enter_text(driver, text, wait_time=1):
    try:
        driver.execute_script('flutter:enterText', text)
        time.sleep(wait_time)
        return True
    except Exception as e:
        print(f"❌ 文本输入失败: {str(e)}")
        return False

# ============================== 测试流程执行 ==============================
# 添加全局配置变量
SHOW_DETAILED_RESULTS = False  # 设置为True则显示详细结果，False只显示最终结果

def execute_test_flow(driver, finder, config_file='test_config.json'):
    results = []
    width, height = 1080, 2400  # 如果无法获取，则使用默认分辨率

    # 右滑 -> 左滑 (从屏幕右侧往左滑)
    start_x = int(width * 0.9)  # 从右边滑动
    end_x = int(width * 0.1)    # 到左边
    y = int(height * 0.5)       # 屏幕中线高度

    for i in range(4):
        cmd = f"adb shell input swipe {start_x} {y} {end_x} {y} 600"
        os.system(cmd)
        print(f"✅ 已完成第 {i+1} 次右滑 -> 左滑 ({cmd})")
    time.sleep(1.2)

    temp_files = []  # 记录所有临时文件
    
    try:
        with open(config_file, 'r', encoding='utf-8') as f:
            config = json.load(f)
        
        for step in config['steps']:
            action = step['type']
            params = step['params']
            
            try:
                if action == 'click_text':
                    success = click_element_by_text(driver, finder, params['target'], params['waitTime'])
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"点击文本: {params['target']} - {'成功' if success else '失败'}")
                elif action == 'click_value_key':
                    success = click_element_by_value_key(driver, finder, params['target'], params['waitTime'])
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"点击值键: {params['target']} - {'成功' if success else '失败'}")
                elif action == 'enter_text':
                    success = enter_text(driver, params['text'], params['waitTime'])
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"输入文本: {params['text']} - {'成功' if success else '失败'}")
                elif action == 'ocr_analysis':
                    ocr_result = ocr_screen_analysis(
                        driver, 
                        keywords=params['keywords'],
                        lang=params.get('lang', 'chi_sim')
                    )
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"OCR分析: {ocr_result}")
                elif action == 'image_tap':
                    result = match_and_tap_center(
                        driver,
                        finder,
                        target_image_path=normalize_path(params['target']),
                        threshold=params.get('threshold', 0.8)
                    )
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"图像点击: {'成功' if result else '失败'}")
                elif action == 'capture_logs':
                    logs = capture_logs(
                        event_key=params['eventKey'],
                        after=params.get('duration', 5)
                    )
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"捕获日志 {len(logs)} 条")
                elif action == 'continuous_check':
                    result = continuous_screenshot_check(
                        driver,
                        keywords=params['keywords'],
                        lang=params.get('lang', 'chi_sim'),
                        count=params.get('count', 5),
                        interval=params.get('interval', 0.5),
                        wait_time = params['waitTime']
                    )
                    # 修改结果展示格式
                    if SHOW_DETAILED_RESULTS:
                        results.append(f"连续检查 - 输入文本: {params.get('text', '无')}\n{result['message']}")
                    else:
                        final_result = '成功' if result['overall_found'] else '失败'
                        results.append(f"{params.get('text', '无')} - 最终结果: {final_result}")
                    
            except Exception as e:
                error_msg = f"步骤执行失败: {action} - {str(e)}"
                print(error_msg)
                results.append(error_msg)
                
        # 测试完成后删除临时配置文件
        if config_file.startswith('temp_test_') and os.path.exists(config_file):
            os.remove(config_file)
            
    except Exception as e:
        error_msg = f"测试执行失败: {str(e)}"
        print(error_msg)
        results.append(error_msg)
    finally:
        # 确保删除所有临时文件
        for temp_file in temp_files:
            if os.path.exists(temp_file):
                os.remove(temp_file)
    
    # 返回所有结果的汇总
    return "\n".join(results)

def continuous_screenshot_check(driver, keywords, lang='chi_sim', save_dir='d:/ocr_screenshots', count=5, interval=0.5 ,wait_time = 1):
    """连续截图并检查是否包含指定文字"""
    os.makedirs(save_dir, exist_ok=True)
    results = []
    screenshots = []  # 记录所有截图路径
    
    for i in range(count):
        timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S_%f")
        screenshot_path = os.path.join(save_dir, f'continuous_{timestamp}.png')
        screenshots.append(screenshot_path)
        
        try:
            # 截图
            driver.save_screenshot(screenshot_path)
            img = Image.open(screenshot_path)
            
            # OCR识别
            text = pytesseract.image_to_string(img, lang=lang, config='--psm 6 --oem 3 -c preserve_interword_spaces=1')
            matches = {keyword: keyword in text for keyword in keywords}
            #print(text)
            results.append({
                'screenshot': screenshot_path,
                'text': text,
                'matches': matches,
                'found': any(matches.values())
            })
            
            time.sleep(interval)
        except Exception as e:
            print(f"第 {i+1} 次截图检查失败: {str(e)}")
            results.append({'error': str(e)})
    
    # 检查5次中是否有任意一次匹配成功
    overall_found = any(result.get('found', False) for result in results)
    if wait_time==500:
       if overall_found :
           overall_found = False
       else:
           overall_found = True
    # 构建结果字符串
    result_str = f"连续检查结果 ({count}次):\n"
    result_str += f"关键词: {', '.join(keywords)}\n"
    result_str += f"最终结果: {'成功' if overall_found else '失败'}\n"
    result_str += "\n详细结果:\n"
    
    for i, result in enumerate(results):
        result_str += f"第{i+1}次: "
        if 'error' in result:
            result_str += f"错误: {result['error']}\n"
        else:
            result_str += f"匹配结果: {result['matches']}\n"
    
    # 清理截图文件
    for screenshot in screenshots:
        if os.path.exists(screenshot):
            os.remove(screenshot)
    
    return {
        'results': results,
        'overall_found': overall_found,
        'message': result_str
    }

def run_test_in_thread(config_file, output_queue=None):  # 添加output_queue参数
    print(f"🔗 开始测试，使用配置文件: {config_file}")
    
    try:
        # 启动新的Appium服务
        appium_process = start_appium_server()
        time.sleep(5)
        
        # 创建新的驱动实例
        driver = webdriver.Remote('http://localhost:4723', options=options)
        finder = FlutterFinder()
        
        # 执行完整测试流程
        result = execute_test_flow(driver, finder, config_file)
        
        # 将结果放入队列
        if output_queue:
            output_queue.put(f"✅ 测试完成\n{result}")
            
    except Exception as e:
        if output_queue:
            output_queue.put(f"❌ 测试失败: {str(e)}")
        print(f"测试失败: {str(e)}")
    finally:
        # 清理本次测试的资源
        if 'driver' in locals():
            driver.quit()
        if 'appium_process' in locals():
            stop_appium_server(appium_process)
        sys.stdout = sys.__stdout__
        sys.stderr = sys.__stderr__
        
        # 删除临时配置文件
        if config_file.startswith('temp_test_') and os.path.exists(config_file):
            os.remove(config_file)

def start_appium_server():
    print("🚀 启动 Appium Server...")
    return subprocess.Popen("cmd /k appium", creationflags=subprocess.CREATE_NEW_CONSOLE)

def stop_appium_server(process):
    if process:
        print("🛑 关闭 Appium Server...")
        subprocess.call(f"taskkill /F /PID {process.pid}", shell=True)


def create_config_generator_interface():
    """创建配置文件生成界面"""
    config_dir = 'd:/ocr_screenshots'
    os.makedirs(config_dir, exist_ok=True)

    # ---------------- 工具函数 ----------------
    def delete_selected_config(selected):
        if not selected:
            return "❌ 请先选择要删除的配置文件", gr.Dropdown()
        
        # 提取文件名（兼容别名在前或在后的格式）
        if " (" in selected:
            filename = selected.split(" (")[-1].rstrip(")")
        else:
            filename = selected
            
        path = os.path.join(config_dir, filename)
        
        try:
            # 删除配置文件
            os.remove(path)
            # 删除别名记录
            aliases = get_aliases()
            if filename in aliases:
                del aliases[filename]
                save_aliases(aliases)
            
            new_choices = get_saved_configs()
            return f"✅ 已删除配置: {filename}", gr.Dropdown(choices=new_choices)
        except Exception as e:
            return f"❌ 删除失败: {str(e)}", gr.Dropdown()

    def start_test_with_current_config(selected, json_text):
        if not selected:
            return "❌ 请先选择或保存一个配置文件"
        
        try:
            # 解析当前JSON内容
            config = json.loads(json_text)
            
            # 创建临时文件
            timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
            temp_config = os.path.join(config_dir, f"temp_test_{timestamp}.json")
            
            with open(temp_config, 'w', encoding='utf-8') as f:
                json.dump(config, f, ensure_ascii=False, indent=2)
            
            # 创建队列用于获取结果
            output_queue = Queue()
            
            # 在新线程中启动测试
            thread = threading.Thread(
                target=run_test_in_thread,
                args=(temp_config, output_queue),
                daemon=True
            )
            thread.start()
            
            # 等待结果并返回
            try:
                result = output_queue.get(timeout=300)  # 5分钟超时
                return result
            except Empty:
                return "❌ 测试超时"
                
        except Exception as e:
            return f"❌ 启动测试失败: {str(e)}"

    def get_aliases():
        alias_path = os.path.join(config_dir, 'config_aliases.json')
        if os.path.exists(alias_path):
            try:
                with open(alias_path, 'r', encoding='utf-8') as f:
                    return json.load(f)
            except:
                return {}
        return {}

    def save_aliases(aliases):
        alias_path = os.path.join(config_dir, 'config_aliases.json')
        with open(alias_path, 'w', encoding='utf-8') as f:
            json.dump(aliases, f, ensure_ascii=False, indent=2)

    def get_saved_configs():
        """获取文件列表 + 别名"""
        aliases = get_aliases()
        config_files = [f for f in os.listdir(config_dir)
                        if f.startswith('test_config_') and f.endswith('.json')]
        result = []
        for f in sorted(config_files, reverse=True):
            if f in aliases:
                result.append(f"{aliases[f]} ({f})")  # 修改这里，将别名放在前面
            else:
                result.append(f)
        return result

    def load_selected_config(selected):
        if not selected:
            return json.dumps({"steps": []}, ensure_ascii=False, indent=2), "❌ 请选择一个配置文件", "当前文件: _(未选择)_", ""
        
        # 修改这里，处理带括号的文件名（无论别名在前还是在后）
        if " (" in selected:
            parts = selected.split(" (")
            filename = parts[-1].rstrip(")")
            alias = " (".join(parts[:-1])
        else:
            filename = selected
            alias = ""
            
        path = os.path.join(config_dir, filename)
        try:
            with open(path, 'r', encoding='utf-8') as f:
                data = json.load(f)
            json_text = json.dumps(data, ensure_ascii=False, indent=2)
            return json_text, f"✅ 已加载配置: {filename}", f"**当前文件:** {filename}", alias
        except Exception as e:
            return json.dumps({"steps": []}, ensure_ascii=False, indent=2), f"❌ 加载失败: {str(e)}", "当前文件: _(未选择)_", ""

    def save_config(json_text, selected):
        if not selected:
            return "❌ 请先选择或新建一个配置文件"
            
        # 提取文件名（兼容别名在前或在后的格式）
        if " (" in selected:
            filename = selected.split(" (")[-1].rstrip(")")
        else:
            filename = selected
            
        path = os.path.join(config_dir, filename)
        try:
            parsed = json.loads(json_text)
            with open(path, 'w', encoding='utf-8') as f:
                json.dump(parsed, f, ensure_ascii=False, indent=2)
            
            # 保存后重新加载配置
            with open(path, 'r', encoding='utf-8') as f:
                data = json.load(f)
            json_text = json.dumps(data, ensure_ascii=False, indent=2)
            
            return f"✅ 已保存修改到: {path}", json_text, f"✅ 已加载配置: {filename}", f"**当前文件:** {filename}"
        except Exception as e:
            return f"❌ 保存失败: {str(e)}", json_text, f"❌ 加载失败: {str(e)}", "当前文件: _(未选择)_"

    def refresh_file_list():
        return gr.Dropdown(choices=get_saved_configs())

    def update_config_alias(selected, new_alias):
        if not selected:
            return "❌ 请先选择配置文件", gr.Dropdown()
        
        # 提取文件名（兼容别名在前或在后的格式）
        if " (" in selected:
            parts = selected.split(" (")
            filename = parts[-1].rstrip(")")
        else:
            filename = selected
            
        aliases = get_aliases()
        aliases[filename] = new_alias
        try:
            save_aliases(aliases)
            new_choices = get_saved_configs()
            
            # 确保新值在选项中
            new_value = f"{new_alias} ({filename})" if new_alias else filename
            if new_value not in new_choices:
                new_choices.insert(0, new_value)
                
            return f"✅ 已更新别名: {filename} -> {new_alias}", gr.Dropdown(
                choices=new_choices,
                value=new_value
            )
        except Exception as e:
            return f"❌ 更新别名失败: {str(e)}", gr.Dropdown()

    def create_new_config():
        timestamp = datetime.datetime.now().strftime("%Y%m%d_%H%M%S")
        filename = f"test_config_{timestamp}.json"
        path = os.path.join(config_dir, filename)
        default_content = {"steps": []}
        with open(path, 'w', encoding='utf-8') as f:
            json.dump(default_content, f, ensure_ascii=False, indent=2)
        json_text = json.dumps(default_content, ensure_ascii=False, indent=2)
        new_choices = get_saved_configs()
        return json_text, gr.Dropdown(choices=new_choices, value=filename), f"✅ 新建配置: {filename}", f"**当前文件:** {filename}", ""

    # ---------------- 步骤操作函数 ----------------
    def add_step_to_json(json_text, action_type, target, wait_time, text_input):
        """在现有 JSON 中添加一个步骤"""
        try:
            config = json.loads(json_text)
            if "steps" not in config:
                config["steps"] = []
            step = {
                "type": action_type,
                "params": {
                    "waitTime": wait_time
                }
            }
            
            if action_type == "enter_text":
                step["params"]["text"] = text_input
            elif action_type == "continuous_check":
                step["params"]["keywords"] = [k.strip() for k in target.split(",")]
                step["params"]["text"] = text_input
            elif action_type == "image_tap":
                step["params"]["target"] = normalize_path(target)  # 确保路径格式正确
                step["params"]["threshold"] = 0.8  # 默认阈值
            else:
                step["params"]["target"] = target
                
            config["steps"].append(step)
            return json.dumps(config, ensure_ascii=False, indent=2), f"✅ 已添加步骤: {action_type}"
        except Exception as e:
            return json_text, f"❌ 添加步骤失败: {str(e)}"

    def stop_test_process():
        try:
            # 关闭Appium服务
            os.system('taskkill /f /im node.exe')
            os.system('taskkill /f /im cmd.exe')
            
            # 关闭adb服务
            os.system('taskkill /f /im adb.exe')
            
            return "✅ 测试进程已关闭"
        except Exception as e:
            return f"❌ 关闭测试进程失败: {str(e)}"

    # ---------------- Gradio 界面 ----------------
    custom_css = """
    .scrollable-code {
        max-height: 400px !important;
        height: 400px !important;
        overflow-y: auto !important;
        margin-bottom: 20px;
        border: 1px solid #e0e0e0;
        border-radius: 8px;
        padding: 10px;
        resize: none;
    }
    .output-box {
        margin-top: 20px;
        border: 1px solid #e0e0e0;
        border-radius: 8px;
        padding: 15px;
        background-color: #f9f9f9;
    }
    """

    with gr.Blocks(css=custom_css) as demo:
        gr.Markdown("## 🧩 测试配置生成器")
    
        with gr.Row():
            with gr.Column(scale=1):
                file_list = gr.Dropdown(
                    label="配置文件列表",
                    choices=get_saved_configs(),
                    interactive=True
                )
                config_alias = gr.Textbox(label="配置别名")
                with gr.Row():
                    update_alias_btn = gr.Button("更新别名")
                    refresh_btn = gr.Button("🔄 刷新列表")
                
                with gr.Row():
                    new_btn = gr.Button("🆕 新建配置")
                    delete_btn = gr.Button("🗑️ 删除配置", variant="stop")
                
                with gr.Row():
                    start_test_btn = gr.Button("🚀 开始测试", variant="primary")
                    stop_test_btn = gr.Button("🛑 关闭测试", variant="secondary")
                
                # 将添加步骤区域移到左侧
                gr.Markdown("### ➕ 添加步骤")
                with gr.Row():
                    action_type = gr.Dropdown(
                        label="操作类型",
                        choices=["click_text", "click_value_key", "enter_text", "ocr_analysis", "image_tap", "capture_logs", "continuous_check"],
                        value="click_text"
                    )
                with gr.Row():
                    target = gr.Textbox(label="目标元素")
                    wait_time = gr.Number(label="等待时间(秒)", value=1)
                text_input = gr.Textbox(label="输入文本（仅 enter_text 类型需要）")
                add_step_btn = gr.Button("添加步骤")
    
            with gr.Column(scale=3):
                with gr.Row():
                    current_file_label = gr.Markdown("**当前文件:** _(未选择)_")
                    save_btn = gr.Button("💾 保存配置", variant="primary")

                # JSON 编辑区
                steps_code = gr.Code(
                    label="当前配置（可直接编辑 JSON）",
                    language="json",
                    interactive=True,
                    value=json.dumps({"steps": []}, ensure_ascii=False, indent=2),
                    elem_classes=["scrollable-code"]
                )
                
                # 操作结果区域
                output = gr.Textbox(
                    label="操作结果", 
                    lines=3,
                    elem_classes=["output-box"]
                )
    
        # ---------------- 绑定事件 ----------------
        file_list.change(
            fn=load_selected_config,
            inputs=file_list,
            outputs=[steps_code, output, current_file_label, config_alias]
        )
    
        refresh_btn.click(
            fn=refresh_file_list,
            outputs=file_list
        )
    
        new_btn.click(
            fn=create_new_config,
            outputs=[steps_code, file_list, output, current_file_label, config_alias]
        )
    
        update_alias_btn.click(
            fn=update_config_alias,
            inputs=[file_list, config_alias],
            outputs=[output, file_list]
        )
    
        save_btn.click(
            fn=save_config,
            inputs=[steps_code, file_list],
            outputs=output
        )
    
        add_step_btn.click(
            fn=add_step_to_json,
            inputs=[steps_code, action_type, target, wait_time, text_input],
            outputs=[steps_code, output]
        )
    
        start_test_btn.click(
            fn=start_test_with_current_config,
            inputs=[file_list, steps_code],
            outputs=output
        )
    
        delete_btn.click(
            fn=delete_selected_config,
            inputs=[file_list],
            outputs=[output, file_list]
        )
    
        stop_test_btn.click(
            fn=stop_test_process,
            outputs=output
        )
    
    return demo

def main():
    demo = create_config_generator_interface()
    demo.launch(server_name="0.0.0.0", server_port=7860, inbrowser=True, share=False)

if __name__ == "__main__":
    main()
