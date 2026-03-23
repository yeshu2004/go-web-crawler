## Web Crawler

A simple and efficient web crawler written in Go. This is designed for crawling web pages and following links to deepen exploration(BFS approch).

## Features

- Multi-threaded crawling for efficiency
- Bloom Filter for Duplicates URL
- Customizable depth and URL filtering
- Graceful handling of robots.txt
- Parsing HTML and extraction of links
- Added comments for easy work flow


## Run 
1. **Set Up Redis Stack with Docker**:
   - Pull the Redis Stack image:
     ```bash
     docker pull redis/redis-stack:latest
     ```
   - Run the Redis Stack container:
     ```bash
     docker run -d -p 6379:6379 --name redis-stack redis/redis-stack:latest
     ```
   - Verify the container is running:
     ```bash
     docker ps
     ```

## Output

<img width="1280" height="797" alt="Screenshot 2026-02-04 at 10 37 23 AM" src="https://github.com/user-attachments/assets/1cc98cfe-54dd-4031-b37a-cfacfcf688a5" />


<img width="1280" height="800" alt="Screenshot 2026-02-04 at 10 37 57 AM" src="https://github.com/user-attachments/assets/4eab4f4c-13f1-4d37-8d8f-8be1fbfd668a" />

<img width="1017" height="200" alt="Screenshot 2026-02-04 at 10 41 31 AM" src="https://github.com/user-attachments/assets/b72150e3-d7a5-43f8-acb0-a5ddf59f1f68" />

<img width="337" height="469" alt="Screenshot 2026-02-04 at 10 41 01 AM" src="https://github.com/user-attachments/assets/8c93a479-3d67-4d3d-818a-2f082906ceba" />



