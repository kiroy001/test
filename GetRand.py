import random
import pymysql
import time
import tornado.httpserver
import tornado.ioloop
import tornado.options
import tornado.web
from redis import Redis
from threading import Lock
'''用于生成大量非重发随机数
CREATE TABLE `t_random_number` (
  `id` int NOT NULL AUTO_INCREMENT,
  `random_number` bigint NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `random_number` (`random_number`)
) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci

'''

# Initialize Redis connection
redis_cli = Redis(host='127.0.0.1', port=6480, db=1)

# Global lock for ensuring atomicity during the generation and insertion process
generation_lock = Lock()

def generate_unique_numbers_and_save_to_file(num, head):
    # Acquire Redis lock to ensure only one thread can execute the process
    redis_lock = redis_cli.lock('generation_lock', timeout=10)

    try:
        # Acquire the lock
        if redis_lock.acquire(blocking=True):
            try:
                # Generate and insert unique numbers
                generated_numbers = generate_and_insert_unique_numbers(num, head)

                # Save the generated numbers to a file
                filename = save_numbers_to_file(generated_numbers)

                return filename

            except Exception as e:
                # Log or handle the exception
                print(f"Error: {e}")
                return None

    except Exception as e:
        # Log or handle the exception
        print(f"Error acquiring Redis lock: {e}")
        return None

    finally:
        # Always release the Redis lock, even if an exception occurs
        if redis_lock.locked():
            redis_lock.release()

def generate_and_insert_unique_numbers(n, head):
    # Connect to MySQL database
    connection = pymysql.connect(
        host="127.0.0.1",
        port=3306,
        user="？？？",
        password="？？？",
        database="？？？",
        charset="utf8",
        autocommit=False  # Disable autocommit to use transactions
    )

    cursor = connection.cursor()

    unique_numbers = set()

    try:
        with generation_lock:
            while len(unique_numbers) < int(n):
                # Generate a 7-digit random number
                random_suffix = random.randint(1000000, 9999999)

                # Concatenate the fixed prefix (first 8 digits) with the random suffix
                generated_number = str(head) + str(random_suffix)

                # Check if the generated number is unique
                cursor.execute("SELECT 1 FROM t_random_number WHERE random_number = %s", (generated_number,))
                if not cursor.fetchone():
                    unique_numbers.add(generated_number)

            # Insert unique numbers into the database within a transaction
            for num in unique_numbers:
                cursor.execute("INSERT INTO t_random_number (random_number) VALUES (%s)", (num,))

            # Commit the transaction
            connection.commit()

    except Exception as e:
        # Rollback the transaction in case of an exception
        connection.rollback()
        raise e

    finally:
        # Close the database connection
        cursor.close()
        connection.close()

    return list(unique_numbers)

def save_numbers_to_file(numbers):
    # Save the numbers to a file (adjust the file path as needed)
    filename = f"/home/web/data/{int(time.time())}_generated_numbers.txt"
    with open(filename, 'w') as file:
        file.write('\n'.join(numbers))
    return filename

class PalpayHandler(tornado.web.RequestHandler):
    def get(self, num, head):
        filename = generate_unique_numbers_and_save_to_file(num, head)
        if filename:
            filename = filename.replace("/home/web", "http://？？？？？")
            self.write(f"生成成功，结果已保存到文件：{filename}")
        else:
            self.write("生成失败，请稍后重试")

if __name__ == "__main__":
    tornado.options.parse_command_line()
    app = tornado.web.Application([
        (r"/rand/(?P<head>.+)/(?P<num>.+)", PalpayHandler),
    ])
    http_server = tornado.httpserver.HTTPServer(app)
    http_server.listen(8091)
    tornado.ioloop.IOLoop.current().start()

