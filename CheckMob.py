import time
from selenium import webdriver
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.common.action_chains import ActionChains
import smtplib
from email.mime.text import MIMEText
from email.mime.image import MIMEImage
from email.mime.multipart import MIMEMultipart
from email.mime.application import MIMEApplication
import os

chrome_options = Options()
chrome_options.add_argument('--headless')
chrome_options.add_argument('--no-sandbox')
chrome_options.add_argument('--disable-dev-shm-usage')
chrome_options.add_argument("--window-size=1920,1080")#处理无头模式下移动鼠标可能有bug
browser = webdriver.Chrome(executable_path='/path/to/chromedriver',options=chrome_options)

browser.get("https://www.mob.com/developer/login")
browser.find_element(By.XPATH, "/html/body/div/div/div/section/div/div[2]/div/div[2]/section/div[3]/div/form/div[1]/div/div/input").send_keys("mob账户")
browser.find_element(By.XPATH, "/html/body/div/div/div/section/div/div[2]/div/div[2]/section/div[3]/div/form/div[2]/div/div/input").send_keys("mob密码")
browser.find_element(By.XPATH, "/html/body/div/div/div/section/div/div[2]/div/div[2]/section/div[3]/div/form/div[3]/div/button").click()
browser.get("https://www.mob.com/")
wait = WebDriverWait(browser, 10)
element = wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, ".service-pull-title")))
element_to_hover_over = browser.find_element(By.CSS_SELECTOR, ".service-pull-title")
actions = ActionChains(browser)#鼠标移动处理
actions.move_to_element(element_to_hover_over).perform()
browser.implicitly_wait(3)
browser.find_element(By.XPATH, "/html/body/div/div/div/header/div/div/div[2]/div[1]/ul/li[1]").click()
time.sleep(5)
browser.get("https://new.dashboard.mob.com/#/summary")
recharge_element = WebDriverWait(browser, 10).until(EC.presence_of_element_located((By.XPATH, "/html/body/div[1]/div[1]/div[2]/div[2]/ul/li[2]/a")))#用于等待页面响应加载

sender_email = "send@qq.com"
receiver_email = "get@qq.com"
password = "key"
text_content = "mob短信余额不足"
mail_content = str(recharge_element.text)[str(recharge_element.text).find("¥") + 1:str(recharge_element.text).find(".")]

if (int(mail_content)<50):
    msg = MIMEMultipart()
    subject = "python 实现邮箱发送邮件"  # 主题
    text = MIMEText(text_content)
    msg.attach(text)
    msg['Subject'] = subject
    msg['From'] = sender_email
    msg['To'] = receiver_email
    s = smtplib.SMTP_SSL("smtp.qq.com", 465)
    s.login(sender_email, password)
    s.sendmail(sender_email, receiver_email, msg.as_string())
    s.quit()
browser.quit()
os.popen("killall chrome")